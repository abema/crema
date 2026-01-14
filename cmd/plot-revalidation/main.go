// Command plot-revalidation generates an SVG plot of the revalidation probability curves.
package main

import (
	"fmt"
	"math"
	"os"
)

const (
	outputPath = "revalidation.svg"
	samples    = 50
)

var maxSecondsList = []int64{30, 60, 120, 300}

func main() {
	if samples < 2 {
		fmt.Fprintln(os.Stderr, "samples must be >= 2")
		os.Exit(1)
	}

	if err := writeSVG(outputPath, maxSecondsList, samples); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func calculateSteepness(targetWindowMillis int64) float64 {
	target := 0.999
	targetMillis := float64(targetWindowMillis)
	return -math.Log(1.0-target) / targetMillis
}

func calculateProbability(steepness float64, remainMillis int64) float64 {
	return 1.0 - math.Exp(-steepness*float64(remainMillis))
}

func writeSVG(path string, maxSecondsList []int64, samples int) error {
	const width = 800
	const height = 480
	const margin = 50
	const tickSize = 6
	const xTicks = 6
	const yTicks = 5
	colors := []string{"#1e5aa8", "#c2432b", "#2c8a45"}

	if len(maxSecondsList) == 0 {
		return fmt.Errorf("maxSecondsList must not be empty")
	}
	maxSeconds := maxSecondsList[0]
	for _, v := range maxSecondsList[1:] {
		if v > maxSeconds {
			maxSeconds = v
		}
	}

	w := width
	h := height
	plotW := w - 2*margin
	plotH := h - 2*margin

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	fmt.Fprintf(f, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n", w, h, w, h)
	fmt.Fprintf(f, `<rect x="0" y="0" width="%d" height="%d" fill="#f7f4ef"/>`+"\n", w, h)
	fmt.Fprintf(f, `<rect x="%d" y="%d" width="%d" height="%d" fill="#ffffff" stroke="#222222" stroke-width="2"/>`+"\n", margin, margin, plotW, plotH)

	fmt.Fprintf(f, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#222222" stroke-width="2"/>`+"\n", margin, h-margin, w-margin, h-margin)
	fmt.Fprintf(f, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#222222" stroke-width="2"/>`+"\n", margin, h-margin, margin, margin)

	fmt.Fprintf(f, `<text x="%d" y="%d" font-family="Verdana" font-size="14" fill="#222222">remainSeconds</text>`+"\n", w/2-45, h-12)
	fmt.Fprintf(f, `<text x="%d" y="%d" font-family="Verdana" font-size="14" fill="#222222">p(t)</text>`+"\n", 16, margin-16)

	for i := 0; i <= xTicks; i++ {
		x := float64(margin) + float64(plotW)*float64(i)/float64(xTicks)
		y := float64(h - margin)
		label := int64(float64(maxSeconds) * float64(i) / float64(xTicks))
		fmt.Fprintf(f, `<line x1="%.2f" y1="%.2f" x2="%.2f" y2="%.2f" stroke="#222222" stroke-width="1"/>`+"\n", x, y, x, y+tickSize)
		fmt.Fprintf(f, `<text x="%.2f" y="%.2f" font-family="Verdana" font-size="12" fill="#222222" text-anchor="middle">%d</text>`+"\n", x, y+tickSize+14, label)
	}

	for i := 0; i <= yTicks; i++ {
		y := float64(margin) + float64(plotH)*float64(i)/float64(yTicks)
		x := float64(margin)
		label := 1.0 - float64(i)/float64(yTicks)
		fmt.Fprintf(f, `<line x1="%.2f" y1="%.2f" x2="%.2f" y2="%.2f" stroke="#222222" stroke-width="1"/>`+"\n", x, y, x-float64(tickSize), y)
		fmt.Fprintf(f, `<text x="%.2f" y="%.2f" font-family="Verdana" font-size="12" fill="#222222" text-anchor="end" dominant-baseline="middle">%.2f</text>`+"\n", x-float64(tickSize)-4, y, label)
	}

	for idx, curveMaxSeconds := range maxSecondsList {
		steepness := calculateSteepness(curveMaxSeconds * 1000)
		color := colors[idx%len(colors)]
		fmt.Fprintf(f, `<polyline fill="none" stroke="%s" stroke-width="3" points="`, color)
		for i := 0; i < samples; i++ {
			tSeconds := float64(curveMaxSeconds) * float64(i) / float64(samples-1)
			tMillis := tSeconds * 1000
			p := calculateProbability(steepness, int64(tMillis))
			x := float64(margin) + (tSeconds/float64(maxSeconds))*float64(plotW)
			y := float64(margin) + (1.0-p)*float64(plotH)
			if i > 0 {
				fmt.Fprint(f, " ")
			}
			fmt.Fprintf(f, "%.2f,%.2f", x, y)
		}
		fmt.Fprintln(f, `"/>`)
		label := fmt.Sprintf("%ds (k=%f)", curveMaxSeconds, steepness)
		legendX := w - margin - 180
		legendY := margin + 16 + idx*18
		fmt.Fprintf(f, `<rect x="%d" y="%d" width="10" height="10" fill="%s"/>`+"\n", legendX, legendY-10, color)
		fmt.Fprintf(f, `<text x="%d" y="%d" font-family="Verdana" font-size="12" fill="#222222">%s</text>`+"\n", legendX+16, legendY, label)
	}

	fmt.Fprintln(f, `</svg>`)
	return nil
}
