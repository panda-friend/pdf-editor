package invoice

import (
	"embed"
	"fmt"
	htmlp "html/template"
	"io/fs"
	"path/filepath"
	"runtime"

	rdpdf "github.com/ledongthuc/pdf"
)

//go:embed pdf
var contentFS embed.FS

//go:embed template
var tmpFS embed.FS

type PDFCreator struct {
	tmpl   *htmlp.Template
	params map[string]string
	images map[string][]byte
	reader func(r *rdpdf.Reader) ([][]string, error)
}

func New(readerFun func(r *rdpdf.Reader) ([][]string, error)) (*PDFCreator, error) {
	c := &PDFCreator{
		reader: readerFun,
		params: make(map[string]string),
	}
	err := c.loadInvoiceTemplate()
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *PDFCreator) loadInvoiceTemplate() error {
	htmlFS, err := fs.Sub(tmpFS, "template")
	if err != nil {
		return fmt.Errorf("couldn't get template dir: %v", err)
	}
	htmlTmpl, err := htmlp.ParseFS(htmlFS, "*.tmpl")
	if err != nil {
		return fmt.Errorf("couldn't parse templates: %v", err)
	}
	images := make(map[string][]byte)
	err = fs.WalkDir(tmpFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		images[path], err = fs.ReadFile(tmpFS, path)
		if err != nil {
			return fmt.Errorf("couldn't read %s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("couldn't read images: %v", err)
	}

	err = fs.WalkDir(contentFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		err = c.parseParamsFromPDF(path)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("couldn't read pdfs: %v", err)
	}

	c.images = images
	c.tmpl = htmlTmpl
	return nil
}

func (c *PDFCreator) parseParamsFromPDF(path string) error {
	f, r, err := rdpdf.Open(filepath.Join("invoice", path))
	defer func() {
		_ = f.Close()
	}()
	if err != nil {
		return err
	}
	content, err := c.reader(r)
	if err != nil {
		return fmt.Errorf("couldn't read %s: %v", path, err)
	}
	pdfHead := []string{
		"MatchX GmbH",
		"Brückenstraße 4",
		"10179 Berlin",
		"Germany",
		"Tax number: 37/436/50071",
		"awesome@matchx.io",
		"VAT ID: DE309834893",
		"INVOICE NUMBER MUST BE INCLUDED WITH YOUR BANK PAYMENT OTHERWISE DELAYS",
		"MAY OCCUR",
		"1 of 1",
	}
	pdfPaymentDetails := []string{
		"Payment details:",
		"Payment must be made within 30 days from issue date.",
		"BIC: PBNKDEFF",
		"IBAN: DE12 1001 0010 0685 1601 27",
		"BANK: Post Bank",
		"ACCOUNT HOLDER: MatchX GmbH",
		"PayPal: info@matchx.io",
	}

	pdfInvoiceDetails := []string{
		"InvoiceNumber",
		"InvoiceDate",
		"OrderDate",
		"OrderNumber",
		"PaymentMethod",
		"ShippingMethod",
	}
	pdfBillTo := []string{
		"BillToName",
		"BillToStreet",
		"BillToCity",
		"BillToCountry",
	}
	pdfShipTo := []string{
		"ShipToName",
		"ShipToStreet",
		"ShipToCity",
		"ShipToCountry",
	}

	nextIdx := 0
	for _, row := range content {
		// match head
		for i := nextIdx; i < nextIdx+len(pdfHead); i++ {
			if row[i] != pdfHead[i] {
				return fmt.Errorf("not able to parse format at row %d", i)
			}
		}
		nextIdx += len(pdfHead)
		// match invoice status
		if row[nextIdx] != "Invoice" {
			return fmt.Errorf("not able to parse format at row %d", len(pdfHead))
		}
		if row[nextIdx+1] == "PAID" {
			//pdfInvoiceStatus = append(pdfInvoiceStatus, "PAID")
			c.params["Status"] = "PAID"
			nextIdx += 2
		}
		runtime.Breakpoint()
		// match invoice details
		oldIdx := nextIdx
		for i := 0; i < len(row); i++ {
			if row[nextIdx+i] == "Bill to:" {
				nextIdx += i + 1
				break
			}
			c.params[pdfInvoiceDetails[i]] = row[nextIdx+i]
		}
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to parse format at row %d", oldIdx)
		}

		// match bill to
		oldIdx = nextIdx
		for i := 0; i < len(row); i++ {
			if row[nextIdx+i] == "Ship to:" {
				nextIdx += i + 1
				break
			}
			c.params[pdfBillTo[i]] = row[nextIdx+i]
		}
		// didn't match beginning of ship to
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to parse format at row %d", oldIdx)
		}
		// match ship to
		oldIdx = nextIdx
		for i := 0; i < len(row); i++ {
			if row[nextIdx+i] == "Description" {
				nextIdx += i + 1
				break
			}
			c.params[pdfShipTo[i]] = row[nextIdx+i]
		}
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to parse format at row %d", oldIdx)
		}
		// match payment details
		oldIdx = nextIdx
		for i := nextIdx; i < len(row); i++ {
			if row[i] == pdfPaymentDetails[0] {
				nextIdx = i
				break
			}
		}
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to parse format at row %d", oldIdx)
		}
		for i := 0; i < len(pdfPaymentDetails); i++ {
			if pdfPaymentDetails[i] != row[nextIdx+i] {
				return fmt.Errorf("not able to parse format at row %d", nextIdx+i)
			}
		}
	}
	return nil
}

func (c *PDFCreator) RegenerateInvoicePDF() error {
	return nil
}
