# Changelog

## 26.10.40

- Add `/api/v1/metrics/_fleet/export` and `/api/v1/metrics/{host}/export` for CSV or `.xlsx` export of supported charts.
- Add direct dependency `github.com/xuri/excelize/v2` (BSD-3-Clause, pure Go) for XLSX generation.
