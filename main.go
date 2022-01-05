package main

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"

	wrpdf "github.com/adrg/go-wkhtmltopdf"

	rdpdf "github.com/ledongthuc/pdf"
	"github.com/panda/pdfeditor/invoice"
)

func main() {
	if err := wrpdf.Init(); err != nil {
		logrus.Error(err)
		os.Exit(1)
	}
	defer wrpdf.Destroy()
	// get rows of content
	pdfCreator, err := invoice.New(ReadPdfInRow)
	if err != nil {
		logrus.Error(err)
		os.Exit(1)
	}

	err = pdfCreator.RecreatePDF()
	if err != nil {
		logrus.Error(err)
		os.Exit(1)
	}
	return
}

func ReadPdfInRow(r *rdpdf.Reader) ([][]string, error) {
	// lines of all content from the pdf file
	content := [][]string{}
	totalPage := r.NumPage()
	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		p := r.Page(pageIndex)
		if p.V.IsNull() {
			continue
		}
		rows, _ := p.GetTextByRow()
		for _, row := range rows {
			joinRow := []string{}
			for _, v := range row.Content {
				if v.S == "" || v.S == "¬" || v.S == "-" {
					continue
				}
				joinRow = append(joinRow, v.S)
				fmt.Println(v.S)
			}
			content = append(content, joinRow)
		}
	}
	return content, nil
}
