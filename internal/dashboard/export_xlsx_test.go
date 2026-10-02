//go:build windows

package dashboard

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestExportXLSXWorkbookStructure(t *testing.T) {
	snap := exportXLSXFixture(ExportTypeFleet)
	contents := writeXLSXParts(t, snap)

	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml", "xl/sharedStrings.xml"} {
		if _, ok := contents[name]; !ok {
			t.Errorf("workbook missing %s", name)
		}
	}
	if len(xlsxWorksheetParts(contents)) != 2 {
		t.Errorf("worksheet count = %d, want 2", len(xlsxWorksheetParts(contents)))
	}
	workbook := string(contents["xl/workbook.xml"])
	data := strings.Index(workbook, `name="Data"`)
	context := strings.Index(workbook, `name="Context"`)
	if data < 0 || context < 0 || data > context {
		t.Fatalf("sheet order = %q, want Data then Context", workbook)
	}
	if !strings.Contains(workbook, `sheetId="`) {
		t.Errorf("workbook sheet ids missing: %q", workbook)
	}

	sheet := xlsxDataSheet(contents)
	for _, header := range []string{"timestamp_utc", "host_name", "series_id", "series_label", "unit", "value", "aggregation", "resolution"} {
		if !strings.Contains(string(contents["xl/sharedStrings.xml"]), ">"+header+"<") {
			t.Errorf("shared strings missing header %q", header)
		}
	}
	if !strings.Contains(sheet, "<pane") || !strings.Contains(sheet, `ySplit="1"`) || !strings.Contains(sheet, `topLeftCell="A2"`) || !strings.Contains(sheet, `activePane="bottomLeft"`) || !strings.Contains(sheet, `state="frozen"`) {
		t.Errorf("Data sheet lacks frozen header pane: %q", sheet)
	}
	if !strings.Contains(sheet, "<autoFilter") || (!strings.Contains(sheet, `ref="A1:H3"`) && !strings.Contains(sheet, `ref="$A$1:$H$3"`)) {
		t.Errorf("Data sheet lacks expected auto filter: %q", sheet)
	}
}

func TestExportXLSXCellTypes(t *testing.T) {
	contents := writeXLSXParts(t, exportXLSXFixture(ExportTypeFleet))
	sheet := xlsxDataSheet(contents)
	if !strings.Contains(sheet, `<c r="F2"><v>42.5</v></c>`) {
		t.Errorf("value cell is not numeric: %q", sheet)
	}
	if !strings.Contains(sheet, `<c r="A2" t="s"><v>`) || !strings.Contains(sheet, `<c r="E2" t="s"><v>`) || !strings.Contains(sheet, `<c r="G2" t="s"><v>`) {
		t.Errorf("text cells are not shared strings: %q", sheet)
	}
	if strings.Contains(sheet, `<c r="B2"`) {
		hostCell := sheet[strings.Index(sheet, `<c r="B2"`):]
		if end := strings.Index(hostCell, "</c>"); end >= 0 {
			hostCell = hostCell[:end]
		}
		if strings.Contains(hostCell, "<v>") {
			t.Errorf("fleet host placeholder must be an empty cell: %q", hostCell)
		}
	}
}

func TestExportXLSXFormulaSafety(t *testing.T) {
	contents := writeXLSXParts(t, exportXLSXFixture(ExportTypeFleet))
	for name, content := range contents {
		if name == "xl/vbaProject.bin" {
			t.Error("workbook includes a VBA project")
		}
		if strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml") {
			xml := string(content)
			if strings.Contains(xml, "<f") {
				t.Errorf("%s contains a formula element", name)
			}
			if strings.Contains(xml, `t="str"`) {
				t.Errorf("%s contains a formula-result string cell", name)
			}
		}
	}
}

func exportXLSXFixture(exportType ExportType) Snapshot {
	value := 42.5
	host := ""
	hostFilter := []string{"host-a", "host-b"}
	rowHost := hostFleetPlaceholder
	if exportType == ExportTypePerHost {
		host = "host-a"
		hostFilter = nil
		rowHost = host
	}
	return Snapshot{
		GraphID: "overview.load", GraphLabel: "Overview Load", GraphSlug: "overview-load",
		ExportType: exportType, HostName: host, HostFilter: hostFilter, Tier: "1min",
		From: time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC),
		DisplayTimezone: "UTC", Coverage: CoverageFull, CoverageNote: "complete",
		Series: []SeriesDefinition{{SeriesID: "cpu_pct", SeriesLabel: "CPU", Unit: "%", Aggregation: "avg", Resolution: "1m"}},
		Rows: []Observation{
			{Timestamp: time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC), HostName: rowHost, SeriesID: "cpu_pct", SeriesLabel: "CPU", Unit: "%", Value: &value, Aggregation: "avg", Resolution: "1m"},
			{Timestamp: time.Date(2026, 9, 30, 14, 1, 0, 0, time.UTC), HostName: rowHost, SeriesID: "cpu_pct", SeriesLabel: "CPU", Unit: "%", Value: nil, Aggregation: "avg", Resolution: "1m"},
		},
	}
}

func writeXLSXParts(t *testing.T, snap Snapshot) map[string][]byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteXLSX(&buf, snap); err != nil {
		t.Fatalf("WriteXLSX: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("invalid XLSX ZIP: %v", err)
	}
	contents := make(map[string][]byte, len(zr.File))
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		contents[file.Name], err = io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
	}
	return contents
}

func xlsxWorksheetParts(contents map[string][]byte) []string {
	parts := make([]string, 0, 2)
	for name := range contents {
		if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
			parts = append(parts, name)
		}
	}
	return parts
}

func xlsxDataSheet(contents map[string][]byte) string {
	for _, name := range xlsxWorksheetParts(contents) {
		if strings.Contains(string(contents[name]), "<autoFilter") {
			return string(contents[name])
		}
	}
	return ""
}
