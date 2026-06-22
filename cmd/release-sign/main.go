// release-sign is the offline tool that produces signed release manifests
// for the DrainCtl auto-updater.
//
//	release-sign keygen --out path\to\release-signing.key \
//	                    [--add-to internal\updater\keys_windows.go]
//	    Generates an Ed25519 keypair. Writes the private key (base64,
//	    one line) to --out with 0600 perms. Prints the public key to
//	    stdout. With --add-to, also inserts the public key into
//	    releaseSigningKeysB64 in the named source file (idempotent — a
//	    second run with the same key is a no-op).
//
//	release-sign sign --key path\to\release-signing.key \
//	                  --msi path\to\LISSTech.DrainCtl.msi \
//	                  --version 26.6.34 \
//	                  --out-dir path\to\release-assets
//	    Hashes --msi, writes release.json + release.json.sig into
//	    --out-dir. Both files are uploaded as GitHub release assets
//	    alongside the MSI.
//
// Build: go run ./cmd/release-sign <subcommand> ...
// (Or `go build -o release-sign.exe ./cmd/release-sign` on Windows.)
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/updater"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		if err := keygen(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "sign":
		if err := sign(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "check-keys-file":
		if err := checkKeysFile(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `release-sign — DrainCtl release manifest signer

  release-sign keygen --out PATH [--add-to FILE]
      Generate Ed25519 keypair; write private key to PATH (0600);
      print public key (base64) to stdout for embedding in
      internal/updater/keys_windows.go. With --add-to, also insert the
      pubkey into releaseSigningKeysB64 in FILE (idempotent).

  release-sign sign --key PATH --msi PATH --version V --out-dir DIR
      Produce DIR/release.json + DIR/release.json.sig signing the SHA-256
      of --msi under version V. Upload both alongside the MSI on GitHub.

  release-sign check-keys-file --keys FILE
      Print the count of non-empty entries in releaseSigningKeysB64 in
      FILE. Exit 0 on well-formed file; exit 1 on parse failure or
      sentinel-slice missing. Used by the just release pipeline to
      decide whether manifest signing is required.`)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "path to write the private key (base64, one line, 0600)")
	addTo := fs.String("add-to", "", "optional Go source file to insert the public key into (e.g. internal/updater/keys_windows.go)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out required")
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("--out %s already exists; refusing to overwrite", *out)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("ed25519.GenerateKey: %w", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	// Write private key as a single base64 line (64 bytes raw → 88 chars).
	// Atomic write via temp + rename so a crash mid-write can't leave a
	// partial key file masquerading as the real one.
	tmp := *out + ".tmp"
	if err := os.WriteFile(tmp, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.Rename(tmp, *out); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename private key: %w", err)
	}

	fmt.Println("Ed25519 keypair generated.")
	fmt.Println("Private key written to:", *out)
	fmt.Println()
	fmt.Println("Public key (base64):")
	fmt.Println("   ", pubB64)
	fmt.Println()

	if *addTo != "" {
		alreadyPresent, err := addKeyToReleaseSigningKeysB64(*addTo, pubB64)
		if err != nil {
			return fmt.Errorf("update %s: %w", *addTo, err)
		}
		if alreadyPresent {
			fmt.Println("Already present in:", *addTo, "— file unchanged.")
		} else {
			fmt.Println("Inserted into:", *addTo)
		}
	} else {
		fmt.Println("Paste the public key as a string entry in releaseSigningKeysB64")
		fmt.Println("inside internal/updater/keys_windows.go:")
		fmt.Printf("    %q,\n", pubB64)
	}
	return nil
}

// checkKeysFile prints the number of non-empty entries in
// releaseSigningKeysB64 in the named Go source file. Exits non-zero
// (via the caller's error return) if the file is malformed or the
// sentinel slice is missing. Used by the `just release` pipeline to
// decide whether manifest signing is required for this build.
func checkKeysFile(args []string) error {
	fs := flag.NewFlagSet("check-keys-file", flag.ExitOnError)
	keys := fs.String("keys", "", "path to internal/updater/keys_windows.go (or fixture)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keys == "" {
		return errors.New("--keys required")
	}
	count, err := countReleaseSigningKeysB64(*keys)
	if err != nil {
		return err
	}
	fmt.Println(count)
	return nil
}

// countReleaseSigningKeysB64 parses the Go source file at path and
// returns the count of non-empty string literal entries in the
// releaseSigningKeysB64 slice. Empty entries (e.g. commented out via
// `""`) are skipped, matching decodeReleaseSigningKeys' behavior.
func countReleaseSigningKeysB64(path string) (int, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return 0, fmt.Errorf("parse: %w", err)
	}
	var slice *ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name == "releaseSigningKeysB64" && i < len(vs.Values) {
				if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
					slice = cl
					return false
				}
			}
		}
		return true
	})
	if slice == nil {
		return 0, errors.New("could not find `var releaseSigningKeysB64 = []string{...}` in file")
	}
	count := 0
	for _, elt := range slice.Elts {
		bl, ok := elt.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			continue
		}
		// Unquote to detect empty string entries (`""` or `` `` ``).
		s, err := strconv.Unquote(bl.Value)
		if err != nil {
			return 0, fmt.Errorf("unquote slice element: %w", err)
		}
		if s != "" {
			count++
		}
	}
	return count, nil
}

// addKeyToReleaseSigningKeysB64 inserts pubKeyB64 into the
// releaseSigningKeysB64 slice literal in the Go source file at path.
// Returns alreadyPresent=true (no file write) if the key is already in
// the slice. Uses go/ast + go/format so the file is parsed and rewritten
// in a way that survives gofmt without textual fragility.
func addKeyToReleaseSigningKeysB64(path, pubKeyB64 string) (alreadyPresent bool, err error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}

	var slice *ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != "releaseSigningKeysB64" {
				continue
			}
			if i >= len(vs.Values) {
				return true
			}
			if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
				slice = cl
				return false
			}
		}
		return true
	})
	if slice == nil {
		return false, errors.New("could not find `var releaseSigningKeysB64 = []string{...}` in file")
	}

	quoted := strconv.Quote(pubKeyB64)
	for _, elt := range slice.Elts {
		if bl, ok := elt.(*ast.BasicLit); ok && bl.Kind == token.STRING && bl.Value == quoted {
			return true, nil
		}
	}

	slice.Elts = append(slice.Elts, &ast.BasicLit{Kind: token.STRING, Value: quoted})

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return false, fmt.Errorf("format: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return false, nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "", "path to the Ed25519 private key (base64)")
	msiPath := fs.String("msi", "", "path to the MSI to sign")
	version := fs.String("version", "", "release version (e.g. 26.6.33)")
	outDir := fs.String("out-dir", "", "directory to write release.json + release.json.sig")
	assetName := fs.String("asset-name", "LISSTech.DrainCtl.msi", "asset filename as published on GitHub")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, p := range []struct{ name, val string }{
		{"--key", *keyPath}, {"--msi", *msiPath}, {"--version", *version}, {"--out-dir", *outDir},
	} {
		if p.val == "" {
			return fmt.Errorf("%s required", p.name)
		}
	}

	priv, err := loadPrivateKey(*keyPath)
	if err != nil {
		return fmt.Errorf("load private key: %w", err)
	}

	hashHex, err := updater.HashFileSHA256(*msiPath)
	if err != nil {
		return fmt.Errorf("hash MSI: %w", err)
	}

	manifest := updater.ReleaseManifest{
		SchemaVersion: updater.ManifestSchemaVersion,
		Version:       strings.TrimSpace(*version),
		Asset:         updater.ManifestAsset{Name: *assetName, SHA256: hashHex},
		SignedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	sig := ed25519.Sign(priv, manifestBytes)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out-dir: %w", err)
	}
	manifestOut := filepath.Join(*outDir, "release.json")
	sigOut := filepath.Join(*outDir, "release.json.sig")

	if err := writeAtomic(manifestOut, manifestBytes, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := writeAtomic(sigOut, sig, 0o644); err != nil {
		return fmt.Errorf("write sig: %w", err)
	}

	fmt.Println("Signed:", manifestOut)
	fmt.Println("       :", sigOut)
	fmt.Println("Asset SHA-256:", hashHex)
	return nil
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key wrong size: got %d, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

func writeAtomic(path string, body []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
