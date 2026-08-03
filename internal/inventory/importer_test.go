package inventory

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestParseCSV(t *testing.T) {
	csv := "Label,BMC Address,BMC Username,BMC Password\n" +
		"cube-1,10.0.0.11,admin,pw1\n" +
		"cube-2,10.0.0.12,root,\n" +
		"\n" + // blank row skipped
		",,,\n" + // blank row skipped
		"bad-no-addr,,admin,pw\n" // missing address -> row error
	res, err := ParseCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Machines) != 2 {
		t.Fatalf("machines = %d (%+v)", len(res.Machines), res.Machines)
	}
	if res.Machines[0].Label != "cube-1" || res.Machines[0].Address != "10.0.0.11" {
		t.Fatalf("row 1 = %+v", res.Machines[0])
	}
	if res.Machines[0].Password == nil || *res.Machines[0].Password != "pw1" {
		t.Fatal("password not parsed")
	}
	// The fully-blank "\n" line is dropped by encoding/csv, so the bad row's
	// reported number reflects the csv reader's record positions.
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "address") {
		t.Fatalf("unexpected error: %+v", res.Errors[0])
	}
}

func TestParseCSVCipher(t *testing.T) {
	csv := "Label,BMC Address,BMC Username,BMC Cipher\n" +
		"cube-1,10.0.0.11,admin,17\n" + // pinned
		"cube-2,10.0.0.12,admin,\n" + // blank -> 0 (try all)
		"cube-3,10.0.0.13,admin,notanumber\n" // invalid -> 0
	res, err := ParseCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Machines) != 3 {
		t.Fatalf("machines = %d", len(res.Machines))
	}
	if res.Machines[0].Cipher != 17 {
		t.Fatalf("row 1 cipher = %d, want 17", res.Machines[0].Cipher)
	}
	if res.Machines[1].Cipher != 0 || res.Machines[2].Cipher != 0 {
		t.Fatalf("blank/invalid cipher not 0: %d %d", res.Machines[1].Cipher, res.Machines[2].Cipher)
	}
}

func TestParseCSVMissingColumn(t *testing.T) {
	_, err := ParseCSV(strings.NewReader("name,ip\nfoo,1.2.3.4\n"))
	if err == nil || !strings.Contains(err.Error(), "label") {
		t.Fatalf("expected missing-label error, got %v", err)
	}
}

func TestParseXLSX(t *testing.T) {
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	f.SetSheetRow(sheet, "A1", &[]string{"label", "bmc_address", "bmc_username", "bmc_password"})
	f.SetSheetRow(sheet, "A2", &[]string{"cube-1", "10.0.0.11", "admin", "pw1"})
	f.SetSheetRow(sheet, "A3", &[]string{"cube-2", "10.0.0.12", "root", ""})
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	res, err := ParseXLSX(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Machines) != 2 || res.Machines[1].Username != "root" {
		t.Fatalf("machines = %+v", res.Machines)
	}
}

func TestImportMany(t *testing.T) {
	s := newStore(t)
	res, _ := ParseCSV(strings.NewReader(
		"label,bmc_address\ncube-1,10.0.0.11\ncube-2,10.0.0.12\n"))
	created, errs := s.ImportMany(res)
	if created != 2 || len(errs) != 0 {
		t.Fatalf("created=%d errs=%+v", created, errs)
	}
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("stored = %d", len(list))
	}
}
