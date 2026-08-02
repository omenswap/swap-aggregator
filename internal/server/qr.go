package server

import (
	"errors"
	"fmt"
	"html/template"
	"strings"

	"rsc.io/qr"
)

const qrQuiet = 2

func qrSVG(text string) (template.HTML, error) {
	if text == "" {
		return "", errors.New("nothing to encode")
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	size := code.Size + qrQuiet*2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="qr" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="deposit address QR code">`, size, size)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, size, size)
	for y := range code.Size {
		// merge horizontal runs so a code is a few dozen rects, not a few thousand
		for x := 0; x < code.Size; x++ {
			if !code.Black(x, y) {
				continue
			}
			run := 1
			for x+run < code.Size && code.Black(x+run, y) {
				run++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="1" fill="#000"/>`, x+qrQuiet, y+qrQuiet, run)
			x += run - 1
		}
	}
	b.WriteString("</svg>")
	return template.HTML(b.String()), nil
}
