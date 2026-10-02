//go:build windows

package dashboard

import (
	"bytes"
	"encoding/csv"
	"testing"

	excelize "github.com/xuri/excelize/v2"
)

func TestExportCrossFormatParity(t *testing.T) {
	snap := exportXLSXFixture(ExportTypePerHost)
	var csvBuffer, xlsxBuffer bytes.Buffer
	if err := WriteCSV(&csvBuffer, snap); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if err := WriteXLSX(&xlsxBuffer, snap); err != nil {
		t.Fatalf("WriteXLSX: %v", err)
	}
	csvRows, err := csv.NewReader(&csvBuffer).ReadAll()
	if err != nil {
		t.Fatalf("read CSV: %v", err)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(xlsxBuffer.Bytes()))
	if err != nil {
		t.Fatalf("open XLSX: %v", err)
	}
	defer workbook.Close()
	xlsxRows, err := workbook.GetRows("Data")
	if err != nil {
		t.Fatalf("read XLSX Data: %v", err)
	}
	if len(csvRows) != len(xlsxRows) {
		t.Fatalf("row count CSV=%d XLSX=%d", len(csvRows), len(xlsxRows))
	}
	for i := range csvRows {
		if len(csvRows[i]) != 8 {
			t.Fatalf("CSV row %d has %d columns", i, len(csvRows[i]))
		}
		for col, want := range csvRows[i] {
			got := ""
			if col < len(xlsxRows[i]) {
				got = xlsxRows[i][col]
			}
			if got != want {
				t.Errorf("row %d col %d XLSX=%q CSV=%q", i, col, got, want)
			}
		}
	}
}
