package invoice

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	htmlp "html/template"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	wrpdf "github.com/adrg/go-wkhtmltopdf"
	rdpdf "github.com/ledongthuc/pdf"
)

//go:embed pdf
var contentFS embed.FS

//go:embed template
var tmpFS embed.FS

type PDFCreator struct {
	tmpl   *htmlp.Template
	images map[string][]byte
	reader func(r *rdpdf.Reader) ([][]string, error)
}

type pdf struct {
	params map[string]interface{}
	tmpl   *htmlp.Template
	images map[string][]byte
	reader func(r *rdpdf.Reader) ([][]string, error)
}

func New(readerFun func(r *rdpdf.Reader) ([][]string, error)) (*PDFCreator, error) {
	c := &PDFCreator{
		reader: readerFun,
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
		if filepath.Ext(path) != ".png" {
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
	c.images = images
	c.tmpl = htmlTmpl
	return nil
}

func (c *PDFCreator) RecreatePDF() error {
	err := fs.WalkDir(contentFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		pdfObj := &pdf{
			params: make(map[string]interface{}),
			tmpl:   c.tmpl,
			images: c.images,
			reader: c.reader,
		}
		err = pdfObj.parseParamsFromPDF(path)
		if err != nil {
			return err
		}
		err = pdfObj.regenerateInvoicePDF(path)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

func compareStringArray(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var pdfHead = []string{
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
var pdfPaymentInfo = []string{
	"Payment details:",
	"Payment must be made within 30 days from issue date.",
	"BIC: PBNKDEFF",
	"IBAN: DE12 1001 0010 0685 1601 27",
	"BANK: Post Bank",
	"ACCOUNT HOLDER: MatchX GmbH",
	"PayPal: info@matchx.io",
}

func (p *pdf) parseParamsFromPDF(path string) error {
	f, r, err := rdpdf.Open(filepath.Join("invoice", path))
	defer func() {
		_ = f.Close()
	}()
	if err != nil {
		return err
	}
	content, err := p.reader(r)
	if err != nil {
		return fmt.Errorf("couldn't read %s: %v", path, err)
	}

	invoiceDetails := []string{
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
		if !compareStringArray(row[nextIdx:nextIdx+len(pdfHead)], pdfHead) {
			return fmt.Errorf("not able to parse pdf head")
		}
		nextIdx += len(pdfHead)
		// match invoice status
		if row[nextIdx] != "Invoice" {
			return fmt.Errorf("not able to parse format at row %s, expect \"Invoice\"", row[nextIdx])
		}
		if row[nextIdx+1] == "PAID" {
			p.params["Status"] = "PAID"
			nextIdx += 2
		}
		// match invoice details
		oldIdx := nextIdx
		for i := 0; i < len(row); i++ {
			if row[nextIdx+i] == "Bill to:" {
				nextIdx += i + 1
				break
			}
			p.params[invoiceDetails[i]] = row[nextIdx+i]
		}
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to detect \"Bill to:\"")
		}

		// match bill to
		oldIdx = nextIdx
		for i := 0; i < len(row); i++ {
			if row[nextIdx+i] == "Ship to:" {
				nextIdx += i + 1
				break
			}
			p.params[pdfBillTo[i]] = row[nextIdx+i]
		}
		// didn't match beginning of ship to
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to detect \"Ship to:\"")
		}
		// match ship to
		oldIdx = nextIdx
		for i := 0; i < len(row); i++ {
			if row[nextIdx+i] == "Description" {
				nextIdx += i + 1
				break
			}
			p.params[pdfShipTo[i]] = row[nextIdx+i]
		}
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to detect \"Description\"")
		}

		// match payment details
		oldIdx = nextIdx
		for i := nextIdx; i < len(row); i++ {
			if row[i] == pdfPaymentInfo[0] {
				nextIdx = i
				break
			}
			if row[i] == "Qty" {
				p.params["Description"] = row[i+2]
				p.params["Quantity"] = row[i+3]
				//p.params["GatewayTotalPrice"] = row[i+4]
			}
			if row[i] == "Discount:" {
				p.params["Discount"] = row[i+1]
			}
			if row[i] == "Shipping:" {
				p.params["Shipping"] = row[i+1]
			}
		}
		if oldIdx == nextIdx {
			return fmt.Errorf("not able to detect %s", pdfPaymentInfo[0])
		}

		if !compareStringArray(row[nextIdx:nextIdx+len(pdfPaymentInfo)], pdfPaymentInfo) {
			return fmt.Errorf("not able to parse payment info")
		}
	}
	return nil
}

func (p *pdf) regenerateInvoicePDF(path string) error {
	template := p.tmpl.Lookup("invoice.pdf-html.tmpl")
	if template == nil {
		return fmt.Errorf("template invoice.pdf-html.tmpl not found")
	}
	p.params["BackgroundImg"] = base64.StdEncoding.EncodeToString(p.images["invoice.pdf001.png"])
	buff := bytes.NewBuffer(nil)
	if err := template.Execute(buff, p.params); err != nil {
		return fmt.Errorf("failed to render template invoice.pdf-html.tmpl: %v", err)
	}

	if err := wrpdf.Init(); err != nil {
		return fmt.Errorf("failted to init pdf library: %v", err)
	}
	defer wrpdf.Destroy()

	object, err := wrpdf.NewObjectFromReader(buff)
	if err != nil {
		return fmt.Errorf("cannot create new pdf object: %v", err)
	}
	converter, err := wrpdf.NewConverter()
	if err != nil {
		return fmt.Errorf("cannot create new converter: %v", err)
	}
	defer converter.Destroy()
	converter.Add(object)
	converter.PaperSize = wrpdf.A4

	// Convert objects and save the output PDF document.
	f, err := os.Stat(filepath.Join("invoice", "new"))
	if err != nil && !os.IsNotExist(err) {
		return err
	} else if (err == nil && !f.IsDir()) || (err != nil && os.IsNotExist(err)) {
		if err := os.MkdirAll(filepath.Join("invoice", "new"), os.FileMode(0755)); err != nil {
			return err
		}
	}

	outFile, err := os.Create(filepath.Join("invoice", "new", filepath.Base(path)))
	if err != nil {
		return fmt.Errorf("failed to create file %s: %v", path, err)
	}
	defer outFile.Close()

	if err := converter.Run(outFile); err != nil {
		log.Fatal(err)
	}
	return nil
}
