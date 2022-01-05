package invoice

import (
	"bytes"
	"embed"
	"fmt"
	htmlp "html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	wrpdf "github.com/adrg/go-wkhtmltopdf"
	rdpdf "github.com/ledongthuc/pdf"
	"github.com/leekchan/accounting"
)

//go:embed pdf
var contentFS embed.FS

//go:embed template
var tmpFS embed.FS

type PDFCreator struct {
	tmpl       *htmlp.Template
	images     map[string][]byte
	reader     func(r *rdpdf.Reader) ([][]string, error)
	pdfObjList []struct {
		path   string
		pdfObj *pdf
	}
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

	invoiceDetails := []string{}
	billTo := []string{}
	shipTo := []string{}

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
			invoiceDetails = append(invoiceDetails, row[nextIdx+i])
		}
		p.params["InvoiceDetailsList"] = invoiceDetails
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
			billTo = append(billTo, row[nextIdx+i])
		}
		p.params["BillToList"] = billTo
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
			shipTo = append(shipTo, row[nextIdx+i])
		}
		p.params["ShipToList"] = shipTo
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
			}
			if row[i] == "Discount:" {
				p.params["Discount"] = strings.ReplaceAll(row[i+1], ".", "")
				p.params["Discount"] = strings.ReplaceAll(p.params["Discount"].(string), ",", ".")
			}
			if row[i] == "Shipping:" {
				p.params["Shipping"] = strings.ReplaceAll(row[i+1], ".", "")
				p.params["Shipping"] = strings.ReplaceAll(p.params["Shipping"].(string), ",", ".")
			}
			if row[i] == "Subtotal:" {
				p.params["GatewayTotalPrice"] = strings.ReplaceAll(row[i+1], ".", "")
				p.params["GatewayTotalPrice"] = strings.ReplaceAll(p.params["GatewayTotalPrice"].(string), ",", ".")
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

type vat struct {
	rateUnderLimit float64
	rateOverLimit  float64
}

var vatMap = map[string]*vat{
	"Germany": {
		rateOverLimit:  0.19,
		rateUnderLimit: 0.19,
	},
	"United States (US)": {
		rateUnderLimit: 0,
		rateOverLimit:  0,
	},
	"Netherlands": {
		rateUnderLimit: 0,
		rateOverLimit:  0,
	},
}

func (p *pdf) getSubTmpl(name string, paramKey string) error {
	var tmpl []string
	params := map[string]interface{}{}
	buff := bytes.NewBuffer(nil)
	for i, v := range p.params[paramKey].([]string) {
		tmpl = append(tmpl, fmt.Sprintf("{{ .Param%d }}<br/>", i))
		params[fmt.Sprintf("Param%d", i)] = v
	}
	if t, err := htmlp.New(name).Parse(strings.Join(tmpl, "")); err != nil {
		return err
	} else {
		if err := t.ExecuteTemplate(buff, name, params); err != nil {
			return err
		}
	}
	p.params[name] = htmlp.HTML(buff.String())
	return nil
}

func getVATRate(country string) *vat {
	rate := vatMap[country]
	if rate == nil {
		return nil
	}
	return rate
}

func (p *pdf) regenerateInvoicePDF(path string) error {
	var err error
	var gatewayTotalPrice, discount, shipping float64
	ac := accounting.Accounting{
		Symbol:    "€",
		Precision: 2,
		Thousand:  ".",
		Decimal:   ",",
	}

	if p.params["GatewayTotalPrice"] != nil {
		gatewayTotalPrice, err = strconv.ParseFloat(p.params["GatewayTotalPrice"].(string), 64)
		if err != nil {
			return err
		}
	}
	if p.params["Discount"] != nil {
		discount, err = strconv.ParseFloat(p.params["Discount"].(string), 64)
		if err != nil {
			return err
		}
		tmpl, err := htmlp.New("discount").Parse(`
<p style="position:absolute;top:592px;left:452px;white-space:nowrap" class="ft10">Discount:</p>
<p style="position:absolute;top:592px;left:741px;white-space:nowrap" class="ft10">{{ .Discount }}</p>`)
		if err != nil {
			return err
		}
		buff := bytes.NewBuffer(nil)
		p.params["Discount"] = fmt.Sprintf("-%s", ac.FormatMoney(discount))
		if err := tmpl.ExecuteTemplate(buff, "discount", p.params); err != nil {
			return err
		}
		p.params["Discount"] = htmlp.HTML(buff.String())
	} else {
		p.params["Discount"] = ""
	}
	if p.params["Shipping"] != nil {
		shipping, err = strconv.ParseFloat(p.params["Shipping"].(string), 64)
		if err != nil {
			return err
		}
	} else {
		p.params["Shipping"] = ""
	}

	var vatRate *vat
	for _, v := range p.params["BillToList"].([]string) {
		vatRate = getVATRate(v)
		if vatRate != nil {
			break
		}
	}
	if vatRate == nil {
		return fmt.Errorf("no vat rate found for billing address: %s", strings.Join(p.params["BillToList"].([]string), "\n"))
	}
	if err = p.getSubTmpl("BillTo", "BillToList"); err != nil {
		return err
	}
	if err = p.getSubTmpl("ShipTo", "ShipToList"); err != nil {
		return err
	}
	if err = p.getSubTmpl("InvoiceDetails", "InvoiceDetailsList"); err != nil {
		return err
	}

	vatTotal := gatewayTotalPrice * vatRate.rateUnderLimit
	gatewayPriceWithoutVAT := gatewayTotalPrice - vatTotal
	totalExclVAT := gatewayTotalPrice + shipping - discount
	total := totalExclVAT + vatTotal

	p.params["Shipping"] = ac.FormatMoney(shipping)
	p.params["GatewayTotalPrice"] = ac.FormatMoney(gatewayPriceWithoutVAT)
	p.params["VATTotal"] = ac.FormatMoney(vatTotal)
	p.params["VATPercentage"] = fmt.Sprintf("%s%%", strconv.FormatFloat(vatRate.rateUnderLimit*100, 'f', 2, 64))
	p.params["TotalExclVAT"] = ac.FormatMoney(totalExclVAT)
	p.params["Total"] = ac.FormatMoney(total)

	template := p.tmpl.Lookup("invoice.pdf-html.tmpl")
	if template == nil {
		return fmt.Errorf("template invoice.pdf-html.tmpl not found")
	}
	buff := bytes.NewBuffer(nil)
	if err := template.Execute(buff, p.params); err != nil {
		return fmt.Errorf("failed to render template invoice.pdf-html.tmpl: %v", err)
	}

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
		return err
	}
	return nil
}
