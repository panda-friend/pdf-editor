package invoice

import "C"
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

	"github.com/sirupsen/logrus"

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
			logrus.Error(err)
			return nil
		}
		err = pdfObj.regenerateInvoicePDF(path)
		if err != nil {
			logrus.Error(err)
			return nil
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

var vatMap = map[string]*vat{
	"Germany":        {rateUnderLimit: 0.19, rateOverLimit: 0.19},
	"Netherlands":    {rateUnderLimit: 0.19, rateOverLimit: 0.21},
	"Austria":        {rateUnderLimit: 0.19, rateOverLimit: 0.20},
	"Belgium":        {rateUnderLimit: 0.19, rateOverLimit: 0.21},
	"Bulgaria":       {rateUnderLimit: 0.19, rateOverLimit: 0.20},
	"Croatia":        {rateUnderLimit: 0.19, rateOverLimit: 0.25},
	"Cyprus":         {rateUnderLimit: 0.19, rateOverLimit: 0.19},
	"Czech Republic": {rateUnderLimit: 0.19, rateOverLimit: 0.21},
	"Denmark":        {rateUnderLimit: 0.19, rateOverLimit: 0.25},
	"Estonia":        {rateUnderLimit: 0.19, rateOverLimit: 0.20},
	"Finland":        {rateUnderLimit: 0.19, rateOverLimit: 0.24},
	"France":         {rateUnderLimit: 0.19, rateOverLimit: 0.20},
	"Greece":         {rateUnderLimit: 0.19, rateOverLimit: 0.24},
	"Hungary":        {rateUnderLimit: 0.19, rateOverLimit: 0.27},
	"Ireland":        {rateUnderLimit: 0.19, rateOverLimit: 0.23},
	"Italy":          {rateUnderLimit: 0.19, rateOverLimit: 0.22},
	"Latvia":         {rateUnderLimit: 0.19, rateOverLimit: 0.21},
	"Lithuania":      {rateUnderLimit: 0.19, rateOverLimit: 0.21},
	"Luxembourg":     {rateUnderLimit: 0.19, rateOverLimit: 0.17},
	"Malta":          {rateUnderLimit: 0.19, rateOverLimit: 0.18},
	"Monaco":         {rateUnderLimit: 0.19, rateOverLimit: 0.20},
	"Poland":         {rateUnderLimit: 0.19, rateOverLimit: 0.23},
	"Portugal":       {rateUnderLimit: 0.19, rateOverLimit: 0.23},
	"Romania":        {rateUnderLimit: 0.19, rateOverLimit: 0.19},
	"Slovakia":       {rateUnderLimit: 0.19, rateOverLimit: 0.20},
	"Slovenia":       {rateUnderLimit: 0.19, rateOverLimit: 0.22},
	"Spain":          {rateUnderLimit: 0.19, rateOverLimit: 0.21},
	"Sweden":         {rateUnderLimit: 0.19, rateOverLimit: 0.25},
	"UK":             {rateUnderLimit: 0.19, rateOverLimit: 0.20},
}
var vatCountryCode = map[string]string{
	"DE": "Germany",
	"NL": "Netherlands",
	"AT": "Austria",
	"BE": "Belgium",
	"BG": "Bulgaria",
	"HR": "Croatia",
	"CY": "Cyprus",
	"CZ": "Czech Republic",
	"DK": "Denmark",
	"EE": "Estonia",
	"FI": "Finland",
	"FR": "France",
	"EL": "Greece",
	"HU": "Hungary",
	"IE": "Ireland",
	"IT": "Italy",
	"LV": "Latvia",
	"LT": "Lithuania",
	"LU": "Luxembourg",
	"MT": "Malta",
	"PL": "Poland",
	"PT": "Portugal",
	"RO": "Romania",
	"SK": "Slovakia",
	"SI": "Slovenia",
	"ES": "Spain",
	"SE": "Sweden",
	"GB": "UK",
	"XI": "UK",
}

func getVATRate(country string) *vat {
	fmt.Println(country)
	if strings.Contains(country, "VAT Number:") {
		country = vatCountryCode[country[12:14]]
	}
	for k, v := range vatMap {
		if strings.Contains(country, k) {
			return v
		}
	}
	return vatMap["Germany"]
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
	}
	if p.params["Shipping"] != nil {
		if p.params["Shipping"].(string) == "Free shipping" {
			shipping = 0
		} else {
			shipping, err = strconv.ParseFloat(p.params["Shipping"].(string), 64)
			if err != nil {
				return err
			}
		}
	}

	country := p.params["BillToList"].([]string)[len(p.params["BillToList"].([]string))-1]
	vatRate := getVATRate(country)
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

	if shipping == 0 {
		p.params["Shipping"] = "Free shipping"
	} else {
		p.params["Shipping"] = ac.FormatMoney(shipping)
	}

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

	// Convert objects and save the output PDF document.
	f, err := os.Stat(filepath.Join("invoice", "new"))
	if err != nil && !os.IsNotExist(err) {
		return err
	} else if (err == nil && !f.IsDir()) || (err != nil && os.IsNotExist(err)) {
		if err := os.MkdirAll(filepath.Join("invoice", "new"), os.FileMode(0755)); err != nil {
			return err
		}
	}
	outFile, err := os.Create(filepath.Join("invoice", "new", fmt.Sprintf("%s.html", filepath.Base(path))))
	if err != nil {
		return fmt.Errorf("failed to create file %s: %v", path, err)
	}
	defer outFile.Close()
	if _, err = outFile.Write(buff.Bytes()); err != nil {
		return err
	}
	if err = outFile.Sync(); err != nil {
		return err
	}

	return nil
}
