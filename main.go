package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/rocketlaunchr/dataframe-go"
	"github.com/wcharczuk/go-chart/v2"
	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/stat"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	ctx := context.Background()

	doneChan := make(chan int, 1)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	profits := [][]int{
		{20, 1}, {40, 1}, {60, 2}, {80, 2},
		{100, 2}, {150, 1}, {200, 1}, {200, 0},
	}
	copyProfitsArr := make([][]int, len(profits))
	copy(copyProfitsArr, profits)

	cfg := MustLoadConfig("config")

	futures.UseTestnet = true
	bc := futures.NewClient(cfg.BinanceAPIKey, cfg.BinanceAPISecret)

	go startTrading(ctx, bc, profits, copyProfitsArr, cfg, doneChan)

	go gracefulShutdown(doneChan, sigChan)

	<-doneChan
}

func startTrading(ctx context.Context, bc *futures.Client, profits, cpProfits [][]int, cfg *Config, doneChan chan int) {
	defer close(doneChan)

	startTime := time.Now()
	timeOut := startTime.Add(time.Hour * 12)
	errCounter := 0

	for time.Now().Before(timeOut) {
		fmt.Println("Скрипт продолжил работу в:", time.Now().Local().Format("15:04:05"))
		if err := Trade(ctx, bc, profits, cpProfits, cfg); err != nil {
			errCounter++
			log.Println(err)
		}
		if errCounter == 5 {
			time.Sleep(4 * time.Minute)
			errCounter = 0
			continue
		}
		time.Sleep(1 * time.Minute)
	}
}

func gracefulShutdown(doneChan chan int, sigChan chan os.Signal) {
	defer close(doneChan)
	<-sigChan
}

// Trade основная торговая стратегия.
func Trade(ctx context.Context, bc *futures.Client, srcProfitsArr, cpProfitArr [][]int, cfg *Config) error {
	pos, err := binanceOpenedPositions(ctx, bc, cfg.Symbol)
	if err != nil {
		return err
	}
	openPosition := pos.Position

	// если нет позиций
	if openPosition == "" {
		fmt.Println("Нет открытых позиций!")
		// закрыть все stop-loss ордера
		_, err = binanceCheckAndCloseOrders(ctx, bc, cfg.Symbol)
		if err != nil {
			return err
		}

		sig, err := checkSignalToBuy(ctx, bc, 100, cfg)
		if err != nil {
			return err
		}

		copy(cpProfitArr, srcProfitsArr)

		if sig == LONG {
			fmt.Printf("Открыта новая позиция: %s\n", LONG)
			err = binanceOpenPosition(ctx, bc, LONG, cfg.MaxPositionAmount, cfg)
			if err != nil {
				return err
			}
		} else if sig == SHORT {
			fmt.Printf("Открыта новая позиция: %s\n", SHORT)
			err = binanceOpenPosition(ctx, bc, SHORT, cfg.MaxPositionAmount, cfg)
			if err != nil {
				return err
			}
		}

	} else {
		entryPrice := pos.EntryPrice
		currentPrice := binanceCryptoPairPrice(ctx, bc, cfg.Symbol)
		quantity := pos.Amount

		fmt.Printf("Найдена открытая позиция: %s - %f\n", openPosition, quantity)

		if openPosition == string(LONG) {
			stopPrice := entryPrice * (1 - cfg.StopPercent)
			if currentPrice < stopPrice {
				// stop-loss
				err = binanceClosePosition(ctx, bc, LONG, math.Abs(quantity), cfg)
				if err != nil {
					return err
				}
				copy(cpProfitArr, srcProfitsArr)
			} else {
				tempArr := make([][]int, len(cpProfitArr))
				copy(tempArr, cpProfitArr)
				for i := range tempArr {
					delta := tempArr[i][0]
					contracts := tempArr[i][1]
					if currentPrice > entryPrice+float64(delta) {
						// забрать профит
						q := math.Abs(cfg.MaxPositionAmount * (float64(contracts) / 10))
						err = binanceClosePosition(ctx, bc, LONG, q, cfg)
						if err != nil {
							return err
						}
						cpProfitArr = cpProfitArr[1:]
					}
				}
			}
		}

		if openPosition == string(SHORT) {
			stopPrice := entryPrice * (1 + cfg.StopPercent)
			if currentPrice > stopPrice {
				// stop-loss
				err = binanceClosePosition(ctx, bc, SHORT, math.Abs(quantity), cfg)
				if err != nil {
					return err
				}
				copy(cpProfitArr, srcProfitsArr)
			} else {
				tempArr := make([][]int, len(cpProfitArr))
				copy(tempArr, cpProfitArr)
				for i := range tempArr {
					delta := tempArr[i][0]
					contracts := tempArr[i][1]
					if currentPrice < entryPrice-float64(delta) {
						// забрать профит
						q := math.Abs(cfg.MaxPositionAmount * (float64(contracts) / 10))
						err = binanceClosePosition(ctx, bc, SHORT, q, cfg)
						if err != nil {
							return err
						}
						cpProfitArr = cpProfitArr[1:]
					}
				}
			}
		}
	}

	return nil
}

// writeKLinesToCsv записывает полученные от биржи свечи и записывает их в csv-файл.
func writeKLinesToCsv(klines []*futures.Kline, filepath string) error {
	csvFile, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer csvFile.Close()

	writer := csv.NewWriter(csvFile)
	defer writer.Flush()

	header := []string{
		"date",
		"open",
		"high",
		"low",
		"close",
		"volume",
	}

	if err = writer.Write(header); err != nil {
		return err
	}

	for _, k := range klines {
		var record []string
		data := []string{
			strconv.FormatInt(k.OpenTime, 10),
			k.Open,
			k.High,
			k.Low,
			k.Close,
			k.Volume,
		}
		record = append(record, data...)
		if err = writer.Write(record); err != nil {
			return err
		}
	}

	return nil
}

// PrepareDataFrame подготавливает датафрейм для дальнейшей работы.
func PrepareDataFrame(df *dataframe.DataFrame) *dataframe.DataFrame {
	// рассчитать индикатор ATR
	df = IndicateATR(dataframe.NewDataFrame(df.Series...), 14)

	// рассчитать и добавить индикатор наклона в датафрейм
	closeVals := df.Series[df.MustNameToColumn("close")].(*dataframe.SeriesFloat64).Values
	slope := IndicateSlope(closeVals, 5)
	_ = df.AddSeries(dataframe.NewSeriesFloat64("slope", nil, slope), nil)

	// рассчитать максимальный канал
	maxChannel := rollingMax(df, "high", 10)
	_ = df.AddSeries(dataframe.NewSeriesFloat64("chan_max", nil, maxChannel), nil)

	// рассчитать минимальный канал
	minChanel := rollingMin(df, "low", 10)
	_ = df.AddSeries(dataframe.NewSeriesFloat64("chan_min", nil, minChanel), nil)

	// рассчитать позицию в канале
	posInChannel := positionInChannel(df, closeVals, minChanel, maxChannel)
	_ = df.AddSeries(dataframe.NewSeriesFloat64("pos_in_chan", nil, posInChannel), nil)

	nRows := df.NRows()
	hccArr, lccArr := make([]float64, nRows), make([]float64, nRows)
	for i := 4; i < nRows-1; i++ {
		if isLocalMaximumIdx(df, i) > 0 {
			hcc := df.Series[df.MustNameToColumn("close")].Value(i).(float64)
			hccArr[i] = hcc
		}
		if isLocalMinimumIdx(df, i) > 0 {
			lcc := df.Series[df.MustNameToColumn("close")].Value(i).(float64)
			lccArr[i] = lcc
		}
	}
	_ = df.AddSeries(dataframe.NewSeriesFloat64("hcc", nil, hccArr), nil)
	_ = df.AddSeries(dataframe.NewSeriesFloat64("lcc", nil, lccArr), nil)

	return df
}

func rollingMax(df *dataframe.DataFrame, column string, size int) []float64 {
	nRows := df.NRows()
	res := make([]float64, nRows)
	for i := 0; i < nRows; i++ {
		start := i - size + 1
		if start < 0 {
			start = 0
		}
		windowMax := math.Inf(-1)
		for j := start; j <= i; j++ {
			val := df.Series[df.MustNameToColumn(column)].(*dataframe.SeriesFloat64).Value(j).(float64)
			if val > windowMax {
				windowMax = val
			}
		}
		res[i] = windowMax
	}
	return res
}

func rollingMin(df *dataframe.DataFrame, column string, size int) []float64 {
	nRows := df.NRows()
	res := make([]float64, nRows)
	for i := 0; i < nRows; i++ {
		start := i - size + 1
		if start < 0 {
			start = 0
		}
		windowMin := math.Inf(1)
		for j := start; j <= i; j++ {
			val := df.Series[df.MustNameToColumn(column)].(*dataframe.SeriesFloat64).Value(j).(float64)
			if val < windowMin {
				windowMin = val
			}
		}
		res[i] = windowMin
	}
	return res
}

func positionInChannel(df *dataframe.DataFrame, closeVals, minChanVals, maxChanVals []float64) []float64 {
	nRows := df.NRows()
	res := make([]float64, nRows)

	for i := 0; i < nRows; i++ {
		res[i] = (closeVals[i] - minChanVals[i]) / (maxChanVals[i] - minChanVals[i])
	}

	return res
}

// IndicateSlope расчет угла наклона графика.
func IndicateSlope(series []float64, n int) []float64 {
	arraySl := make([]float64, len(series))
	for j := n; j <= len(series); j++ {
		y := series[j-n : j]
		x := make([]float64, n)
		for i := range x {
			x[i] = float64(i)
		}

		xMin, xMax := mat.Min(mat.NewVecDense(len(x), x)), mat.Max(mat.NewVecDense(len(x), x))
		yMin, yMax := mat.Min(mat.NewVecDense(len(y), y)), mat.Max(mat.NewVecDense(len(y), y))
		xSc := make([]float64, n)
		ySc := make([]float64, n)
		for i := range x {
			xSc[i] = (x[i] - xMin) / (xMax - xMin)
			ySc[i] = (y[i] - yMin) / (yMax - yMin)
		}

		// вычисление линейной регрессии
		_, slope := stat.LinearRegression(xSc, ySc, nil, false)

		arraySl[j-1] = slope
	}

	slopeAngle := make([]float64, len(arraySl))
	for i, slope := range arraySl {
		slopeAngle[i] = math.Atan(slope) * (180.0 / math.Pi)
	}

	return slopeAngle
}

// IndicateATR расчет индикатора ATR.
func IndicateATR(df *dataframe.DataFrame, period int) *dataframe.DataFrame {
	df = df.Copy()
	nRows := df.NRows()

	high := df.Series[df.MustNameToColumn("high")].(*dataframe.SeriesFloat64).Values
	low := df.Series[df.MustNameToColumn("low")].(*dataframe.SeriesFloat64).Values
	closes := df.Series[df.MustNameToColumn("close")].(*dataframe.SeriesFloat64).Values

	// Расчет компонентов TR
	hl := make([]float64, nRows)
	hpc := make([]float64, nRows)
	lpc := make([]float64, nRows)
	tr := make([]float64, nRows)

	for i := 0; i < nRows; i++ {
		hl[i] = high[i] - low[i]
		if i > 0 {
			hpc[i] = math.Abs(high[i] - closes[i-1])
			lpc[i] = math.Abs(low[i] - closes[i-1])
			// Вычисление TR, путем выбора максимального значения среди hl, hpc, lpc
			tr[i] = math.Max(hl[i], math.Max(hpc[i], lpc[i]))
		}
	}

	// Расчет ATR
	atr := make([]float64, nRows)
	for i := period; i < nRows; i++ {
		sum := 0.0
		for j := i - period + 1; j <= i; j++ {
			sum += tr[j]
		}
		atr[i] = sum / float64(period)
	}

	// Создаем новый DataFrame для хранения результатов
	trSeries := dataframe.NewSeriesFloat64("TR", nil, tr)
	atrSeries := dataframe.NewSeriesFloat64("ATR", nil, atr)
	_ = df.AddSeries(trSeries, nil)
	_ = df.AddSeries(atrSeries, nil)

	return df
}

func isLocalMinimumIdx(df *dataframe.DataFrame, idx int) int {
	df = df.Copy()
	localMin := 0

	closeSer := df.Series[df.MustNameToColumn("close")]

	if closeSer.Value(idx).(float64) <= closeSer.Value(idx+1).(float64) &&
		closeSer.Value(idx).(float64) <= closeSer.Value(idx-1).(float64) &&
		closeSer.Value(idx-1).(float64) < closeSer.Value(idx+1).(float64) {
		localMin = idx - 1
	}

	return localMin
}

func isLocalMaximumIdx(df *dataframe.DataFrame, idx int) int {
	df = df.Copy()
	localMax := 0

	closeSer := df.Series[df.MustNameToColumn("close")]

	if closeSer.Value(idx).(float64) >= closeSer.Value(idx+1).(float64) &&
		closeSer.Value(idx).(float64) >= closeSer.Value(idx-1).(float64) &&
		closeSer.Value(idx-1).(float64) > closeSer.Value(idx+1).(float64) {
		localMax = idx
	}

	return localMax
}

func MinMaxChannel(df *dataframe.DataFrame, n int) (min float64, max float64) {
	nRows := df.NRows()
	highSeries := df.Series[df.MustNameToColumn("high")]
	lowSeries := df.Series[df.MustNameToColumn("low")]
	for i := 0; i < n; i++ {
		high := highSeries.Value(nRows - i).(float64)
		low := lowSeries.Value(nRows - i).(float64)
		if high > max {
			max = high
		}
		if low < min {
			min = low
		}
	}
	return
}

// checkSignalToBuy проверяет и находит места выгодные для покупки.
func checkSignalToBuy(ctx context.Context, bc *futures.Client, limit int, cfg *Config) (TradingPosition, error) {
	var sig TradingPosition

	// Текущая свеча - (limit-1), которая ещё не закрыта,
	// (limit-2) - последняя закрытая свеча.
	// Нам необходима свеча (limit-3), чтобы проверить, верх это или низ.
	lastCandle := limit - 2
	if lastCandle <= 0 {
		return sig, fmt.Errorf("limit must be greater than 2")
	}

	ohlc, err := binanceKlinesFuturesDataframe(ctx, bc, limit, cfg)
	if err != nil {
		return sig, err
	}

	df := PrepareDataFrame(ohlc)
	posInChanIdx := df.MustNameToColumn("pos_in_chan")
	slopeIdx := df.MustNameToColumn("slope")

	saveChartAsSVG(df, 1, float64(limit))

	if isLocalMinimumIdx(df, lastCandle-1) > 0 {
		// найден низ, значит открыть LONG позицию
		if df.Series[posInChanIdx].Value(lastCandle-1).(float64) < 0.5 {
			// закрыть по верхней границе канала
			slope, valid := df.Series[slopeIdx].Value(lastCandle - 1).(float64)
			if !valid {
				return sig, fmt.Errorf("get slope error: %v", df)
			}
			if slope < -20 {
				// найдена хорошая точка входа для LONG
				sig = LONG
			}
		}
	}

	if isLocalMaximumIdx(df, lastCandle-1) > 0 {
		// найден верх, значит открыть SHORT позицию
		if df.Series[posInChanIdx].Value(lastCandle-1).(float64) > 0.5 {
			// закрыть по верхней позиции канала
			if df.Series[slopeIdx].Value(lastCandle-1).(float64) > 20 {
				sig = SHORT
			}
		}
	}

	return sig, nil
}

// saveChartAsSVG сохраняет график каналов как векторное изображение,
// принимая минимальный и максимальный номер свечи.
func saveChartAsSVG(df *dataframe.DataFrame, min, max float64) {
	chanMax := df.Series[df.MustNameToColumn("chan_max")].(*dataframe.SeriesFloat64)
	closes := df.Series[df.MustNameToColumn("close")].(*dataframe.SeriesFloat64)
	chanMin := df.Series[df.MustNameToColumn("chan_min")].(*dataframe.SeriesFloat64)
	xValues := chart.Seq{Sequence: chart.NewLinearSequence().WithStart(min).WithEnd(max)}.Values()
	graph := chart.Chart{
		Series: []chart.Series{
			chart.ContinuousSeries{
				XValues: xValues,
				YValues: chanMax.Values,
			},
			chart.ContinuousSeries{
				XValues: xValues,
				YValues: closes.Values,
			},
			chart.ContinuousSeries{
				XValues: xValues,
				YValues: chanMin.Values,
			},
		},
	}

	f, _ := os.Create("./images/output.svg")
	defer f.Close()
	_ = graph.Render(chart.SVG, f)
}
