//go:build windows

// Package dashboard — export pipeline scaffolding.
//
// This file pins the excelize/v2 dependency and reserves the package surface
// for the chart data export feature (015-chart-data-export). The actual
// snapshot, CSV, and XLSX writers land in sibling files (export_snapshot.go,
// export_csv.go, export_xlsx.go) and the HTTP wiring in handlers_export.go.
//
// excelize is referenced from a package-level variable so that `go mod tidy`
// keeps it as a direct dependency until the real XLSX writer replaces this
// scaffolding (T023).
package dashboard

import (
	excelize "github.com/xuri/excelize/v2"
)

// excelizeScaffold is the placeholder pin used while the real XLSX writer
// (T023) is being built. It is replaced in T023 by the production
// WriteXLSX function in export_xlsx.go.
var excelizeScaffold = func() *excelize.File {
	return nil
}
