package inventory

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Import columns (header row, case-insensitive; spaces or underscores).
const (
	colLabel    = "label"
	colAddress  = "bmc_address"
	colUsername = "bmc_username"
	colPassword = "bmc_password"
	colCipher   = "bmc_cipher" // optional: pin the IPMI cipher suite (e.g. 17)
)

// RowError reports a rejected row (1-based, including the header row).
type RowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ImportResult summarizes a parse.
type ImportResult struct {
	Machines []Input
	Errors   []RowError
}

func normHeader(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_")
}

// headerIndex maps known columns to their position; label + address required.
func headerIndex(header []string) (map[string]int, error) {
	idx := map[string]int{}
	for i, h := range header {
		idx[normHeader(h)] = i
	}
	if _, ok := idx[colLabel]; !ok {
		return nil, fmt.Errorf("missing %q column", colLabel)
	}
	if _, ok := idx[colAddress]; !ok {
		return nil, fmt.Errorf("missing %q column", colAddress)
	}
	return idx, nil
}

func rowToInput(idx map[string]int, cells []string) (Input, bool) {
	get := func(key string) string {
		i, ok := idx[key]
		if !ok || i >= len(cells) {
			return ""
		}
		return strings.TrimSpace(cells[i])
	}
	in := Input{
		Label:    get(colLabel),
		Address:  get(colAddress),
		Username: get(colUsername),
	}
	if _, ok := idx[colPassword]; ok {
		pw := get(colPassword)
		in.Password = &pw
	}
	if c := get(colCipher); c != "" {
		if n, err := strconv.Atoi(c); err == nil {
			in.Cipher = n
		}
	}
	// Skip fully blank rows.
	if in.Label == "" && in.Address == "" {
		return Input{}, false
	}
	return in, true
}

func rowsToResult(idx map[string]int, dataRows [][]string, headerOffset int) ImportResult {
	res := ImportResult{}
	for i, cells := range dataRows {
		in, ok := rowToInput(idx, cells)
		if !ok {
			continue
		}
		if err := validate(in); err != nil {
			res.Errors = append(res.Errors, RowError{Row: i + headerOffset + 1, Message: err.Error()})
			continue
		}
		res.Machines = append(res.Machines, in)
	}
	return res
}

// ParseCSV parses machines from a CSV reader.
func ParseCSV(r io.Reader) (ImportResult, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return ImportResult{}, err
	}
	if len(records) == 0 {
		return ImportResult{}, fmt.Errorf("empty file")
	}
	idx, err := headerIndex(records[0])
	if err != nil {
		return ImportResult{}, err
	}
	// header is row 1; data starts at row 2.
	return rowsToResult(idx, records[1:], 1), nil
}

// ParseXLSX parses machines from the first sheet of an .xlsx reader.
func ParseXLSX(r io.Reader) (ImportResult, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return ImportResult{}, err
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return ImportResult{}, fmt.Errorf("no sheets in workbook")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return ImportResult{}, err
	}
	if len(rows) == 0 {
		return ImportResult{}, fmt.Errorf("empty sheet")
	}
	idx, err := headerIndex(rows[0])
	if err != nil {
		return ImportResult{}, err
	}
	return rowsToResult(idx, rows[1:], 1), nil
}

// ImportMany creates all machines from an ImportResult, returning how many
// were created and appending any per-row create errors.
func (s *Store) ImportMany(res ImportResult) (created int, errs []RowError) {
	errs = res.Errors
	for i, in := range res.Machines {
		if _, err := s.Create(in); err != nil {
			errs = append(errs, RowError{Row: i + 2, Message: err.Error()})
			continue
		}
		created++
	}
	return created, errs
}

// CSVTemplate is a ready-to-fill import template.
const CSVTemplate = "label,bmc_address,bmc_username,bmc_password\n" +
	"cube-1,10.0.0.11,admin,changeme\n"
