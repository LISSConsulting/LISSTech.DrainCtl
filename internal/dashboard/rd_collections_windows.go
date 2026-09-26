//go:build windows

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/winexec"
)

const (
	rdCollectionRefreshInterval      = 5 * time.Minute
	rdCollectionCommandTimeout       = 30 * time.Second
	rdCollectionCommandStderrMaxSize = 2 * 1024
	rdConnectionBrokerEnv            = "DRAINCTL_RD_CONNECTION_BROKER"
)

// rdCollectionPowerShell emits a JSON array even if no session collections
// exist. The broker is read only from the process environment so its value is
// never interpolated into PowerShell source.
const rdCollectionPowerShell = `$ErrorActionPreference = 'Stop'
Import-Module RemoteDesktop
$broker = $env:DRAINCTL_RD_CONNECTION_BROKER
$collections = if ([string]::IsNullOrWhiteSpace($broker)) { Get-RDSessionCollection } else { Get-RDSessionCollection -ConnectionBroker $broker }
$records = @(
    $collections | ForEach-Object {
        $collection = $_.CollectionName
        $hosts = if ([string]::IsNullOrWhiteSpace($broker)) { Get-RDSessionHost -CollectionName $collection } else { Get-RDSessionHost -CollectionName $collection -ConnectionBroker $broker }
        $hosts | ForEach-Object { [pscustomobject]@{ host = $_.SessionHost; collection = $collection } }
    }
)
ConvertTo-Json -InputObject $records -Compress`

type rdCollectionLoader func(context.Context, string) ([]byte, error)

// RDCollectionProbeResult summarizes a successful RD Session Collection
// discovery without changing the resolver snapshot or persisted configuration.
type RDCollectionProbeResult struct {
	CollectionCount  int
	SessionHostCount int
}

// ProbeRDSessionCollections verifies that the configured Connection Broker can
// enumerate its RD Session Collections. It is intentionally side-effect free.
func ProbeRDSessionCollections(ctx context.Context, broker string) (RDCollectionProbeResult, error) {
	return probeRDSessionCollections(ctx, broker, loadRDCollections)
}

func probeRDSessionCollections(ctx context.Context, broker string, loader rdCollectionLoader) (RDCollectionProbeResult, error) {
	data, err := loader(ctx, broker)
	if err != nil {
		return RDCollectionProbeResult{}, err
	}
	records, err := decodeRDCollectionRecords(data)
	if err != nil {
		return RDCollectionProbeResult{}, err
	}

	collections := make(map[string]struct{}, len(records))
	hosts := make(map[string]struct{}, len(records))
	for _, record := range records {
		collections[strings.TrimSpace(record.Collection)] = struct{}{}
		hosts[normalizeRDCollectionHost(record.Host)] = struct{}{}
	}
	return RDCollectionProbeResult{
		CollectionCount:  len(collections),
		SessionHostCount: len(hosts),
	}, nil
}

type rdCollectionRecord struct {
	Host       string `json:"host"`
	Collection string `json:"collection"`
}

type rdCollectionSnapshot struct {
	exact map[string]rdCollectionValue
	short map[string]rdCollectionValue
}

type rdCollectionValue struct {
	collection  string
	valid       bool
	shortSource bool
}

// rdCollectionResolver periodically loads the RDS host-to-collection mapping.
// Its snapshot is replaced only after a complete, valid loader response.
type rdCollectionResolver struct {
	loader          rdCollectionLoader
	refreshInterval time.Duration

	snapshot atomic.Pointer[rdCollectionSnapshot]

	brokerMu          sync.RWMutex
	broker            string
	brokerInitialized bool
	trigger           chan struct{}
}

func newRDCollectionCommand(ctx context.Context, broker string) *exec.Cmd {
	cmd := winexec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", rdCollectionPowerShell)
	cmd.Env = rdCollectionEnvironment(broker)
	return cmd
}

func newRDCollectionResolver(loader rdCollectionLoader) *rdCollectionResolver {
	if loader == nil {
		loader = loadRDCollections
	}
	return &rdCollectionResolver{
		loader:          loader,
		refreshInterval: rdCollectionRefreshInterval,
		trigger:         make(chan struct{}, 1),
	}
}

// Run refreshes immediately, then every five minutes and after broker updates.
// Discovery errors intentionally leave the last successful snapshot available.
func (r *rdCollectionResolver) Run(ctx context.Context, initialBroker string) {
	r.brokerMu.Lock()
	if !r.brokerInitialized {
		r.broker = strings.TrimSpace(initialBroker)
		r.brokerInitialized = true
	}
	r.brokerMu.Unlock()
	r.refresh(ctx)

	ticker := time.NewTicker(r.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refresh(ctx)
		case <-r.trigger:
			r.refresh(ctx)
		}
	}
}

// UpdateBroker changes the target Connection Broker and schedules one
// coalesced refresh. An empty broker asks the local host to act as broker.
func (r *rdCollectionResolver) UpdateBroker(broker string) {
	r.setBroker(broker)
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// CollectionFor returns the collection for host. It accepts exact FQDNs and
// an unambiguous short name, case-insensitively. Unknown or ambiguous hosts
// return an empty string.
func (r *rdCollectionResolver) CollectionFor(host string) string {
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return ""
	}
	key := normalizeRDCollectionHost(host)
	if value, ok := snapshot.exact[key]; ok {
		if value.valid {
			return value.collection
		}
		return ""
	}
	if value, ok := snapshot.short[rdCollectionShortName(key)]; ok && value.valid &&
		(!strings.Contains(key, ".") || value.shortSource) {
		return value.collection
	}
	return ""
}

func (r *rdCollectionResolver) refresh(ctx context.Context) {
	r.brokerMu.RLock()
	broker := r.broker
	r.brokerMu.RUnlock()

	data, err := r.loader(ctx, broker)
	if err != nil {
		slog.Warn("dashboard: RD Session Collection discovery failed", "error", err)
		return
	}

	records, err := decodeRDCollectionRecords(data)
	if err != nil {
		slog.Warn("dashboard: RD Session Collection discovery returned invalid JSON", "error", err)
		return
	}
	r.snapshot.Store(buildRDCollectionSnapshot(records))
}

func decodeRDCollectionRecords(data []byte) ([]rdCollectionRecord, error) {
	var records []rdCollectionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, errors.New("expected JSON array")
	}
	for _, record := range records {
		if normalizeRDCollectionHost(record.Host) == "" || strings.TrimSpace(record.Collection) == "" {
			return nil, errors.New("malformed RD Session Collection record")
		}
	}
	return records, nil
}

func (r *rdCollectionResolver) setBroker(broker string) {
	r.brokerMu.Lock()
	r.broker = strings.TrimSpace(broker)
	r.brokerInitialized = true
	r.brokerMu.Unlock()
}

func buildRDCollectionSnapshot(records []rdCollectionRecord) *rdCollectionSnapshot {
	snapshot := &rdCollectionSnapshot{
		exact: make(map[string]rdCollectionValue),
		short: make(map[string]rdCollectionValue),
	}
	shortHosts := make(map[string]map[string]struct{})

	for _, record := range records {
		host := normalizeRDCollectionHost(record.Host)
		collection := strings.TrimSpace(record.Collection)
		if host == "" || collection == "" {
			continue
		}

		if existing, ok := snapshot.exact[host]; ok && (!existing.valid || existing.collection != collection) {
			snapshot.exact[host] = rdCollectionValue{}
		} else if !ok {
			snapshot.exact[host] = rdCollectionValue{
				collection:  collection,
				valid:       true,
				shortSource: !strings.Contains(host, "."),
			}
		}

		if short := rdCollectionShortName(host); short != "" {
			if shortHosts[short] == nil {
				shortHosts[short] = make(map[string]struct{})
			}
			shortHosts[short][host] = struct{}{}
		}
	}

	for short, hosts := range shortHosts {
		if len(hosts) != 1 {
			continue
		}
		for host := range hosts {
			if value := snapshot.exact[host]; value.valid {
				snapshot.short[short] = value
			}
		}
	}
	return snapshot
}

func normalizeRDCollectionHost(host string) string {
	host = strings.TrimSpace(host)
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func rdCollectionShortName(host string) string {
	short, _, _ := strings.Cut(host, ".")
	return short
}

func loadRDCollections(ctx context.Context, broker string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, rdCollectionCommandTimeout)
	defer cancel()

	return runRDCollectionCommand(newRDCollectionCommand(commandCtx, broker))
}

// runRDCollectionCommand captures stdout and stderr independently so only the
// PowerShell JSON stream is passed to the strict decoder.
func runRDCollectionCommand(cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, wrapRDCollectionCommandError(err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func wrapRDCollectionCommandError(err error, stderr string) error {
	diagnostic := strings.TrimSpace(stderr)
	if diagnostic == "" {
		return fmt.Errorf("discover RD Session Collections: %w", err)
	}
	if len(diagnostic) > rdCollectionCommandStderrMaxSize {
		diagnostic = strings.TrimSpace(diagnostic[:rdCollectionCommandStderrMaxSize])
	}
	return fmt.Errorf("discover RD Session Collections: %w: %s", err, diagnostic)
}

func rdCollectionEnvironment(broker string) []string {
	environment := os.Environ()
	prefix := rdConnectionBrokerEnv + "="
	filtered := environment[:0]
	for _, entry := range environment {
		if !strings.HasPrefix(strings.ToUpper(entry), prefix) {
			filtered = append(filtered, entry)
		}
	}
	if broker != "" {
		filtered = append(filtered, prefix+broker)
	}
	return filtered
}
