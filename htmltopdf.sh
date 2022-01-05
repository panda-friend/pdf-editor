#!/bin/bash

go build -o pdf-editor main.go
./pdf-editor

exit

html_files=$(ls invoice/new/*.html)
for item in $html_files;
do
	wkhtmltopdf $item "$(echo $item|sed 's/.html//g')"
done

rm invoice/new/*.html