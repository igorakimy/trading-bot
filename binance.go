package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/rocketlaunchr/dataframe-go"
	"github.com/rocketlaunchr/dataframe-go/imports"
	"os"
	"strconv"
)

type OpenedPosition struct {
	Position   string
	Amount     float64
	Profit     float64
	Leverage   float64
	Balance    float64
	EntryPrice float64
	Other      float64
}

type TradingPosition string

const (
	LONG  TradingPosition = "long"
	SHORT TradingPosition = "short"
)

// binanceKlinesFuturesDataframe получает последние limit свечей
// для указанной валютной пары в opts.
func binanceKlinesFuturesDataframe(ctx context.Context, bc *futures.Client, limit int, cfg *Config) (*dataframe.DataFrame, error) {
	klines, err := bc.NewKlinesService().
		Limit(limit).
		Symbol(cfg.Symbol).
		Interval(cfg.Interval).
		Do(ctx)
	if err != nil {
		return nil, err
	}

	if err = writeKLinesToCsv(klines, cfg.KlinesCsvFile); err != nil {
		return nil, err
	}

	file, _ := os.Open(cfg.KlinesCsvFile)
	df, _ := imports.LoadFromCSV(ctx, file, imports.CSVLoadOptions{
		InferDataTypes: true,
	})

	df.Sort(ctx, []dataframe.SortKey{
		{Key: "date", Desc: true},
	})

	return df, nil
}

// binanceOpenPosition открывает торговую позицию на указанное кол-во валюты
func binanceOpenPosition(ctx context.Context, bc *futures.Client, position TradingPosition, quantity float64, cfg *Config) error {
	cryptoPairPrice := binanceCryptoPairPrice(ctx, bc, cfg.Symbol)
	var closePrice string
	var sideType futures.SideType

	if position == LONG {
		closePrice = fmt.Sprintf("%.2f", cryptoPairPrice*(1+0.01))
		sideType = futures.SideTypeBuy
	} else if position == SHORT {
		closePrice = fmt.Sprintf("%.2f", cryptoPairPrice*(1-0.01))
		sideType = futures.SideTypeSell
	} else {
		return fmt.Errorf("unsupported position type")
	}

	_, err := bc.NewCreateBatchOrdersService().
		OrderList([]*futures.CreateOrderService{
			bc.NewCreateOrderService().
				Symbol(cfg.Symbol).
				Side(sideType).
				Type(futures.OrderTypeLimit).
				Quantity(fmt.Sprintf("%f", quantity)).
				TimeInForce(futures.TimeInForceTypeGTC).
				Price(closePrice),
		}).
		Do(ctx)
	if err != nil {
		return err
	}

	return nil
}

// binanceOpenPosition закрывает торговую позицию на указанное кол-во валюты
func binanceClosePosition(ctx context.Context, bc *futures.Client, position TradingPosition, quantity float64, cfg *Config) error {
	cryptoPairPrice := binanceCryptoPairPrice(ctx, bc, cfg.Symbol)
	var closePrice string
	var sideType futures.SideType

	if position == LONG {
		closePrice = fmt.Sprintf("%.2f", cryptoPairPrice*(1-0.01))
		sideType = futures.SideTypeSell
	} else if position == SHORT {
		closePrice = fmt.Sprintf("%.2f", cryptoPairPrice*(1+0.01))
		sideType = futures.SideTypeBuy
	} else {
		return fmt.Errorf("unsupported position type")
	}

	_, err := bc.NewCreateBatchOrdersService().
		OrderList([]*futures.CreateOrderService{
			bc.NewCreateOrderService().
				Symbol(cfg.Symbol).
				Side(sideType).
				Type(futures.OrderTypeLimit).
				Quantity(fmt.Sprintf("%f", quantity)).
				TimeInForce(futures.TimeInForceTypeGTC).
				Price(closePrice),
		}).
		Do(ctx)
	if err != nil {
		return err
	}

	return nil
}

// binanceOpenedPositions получает информацию о всех открытых позициях.
func binanceOpenedPositions(ctx context.Context, bc *futures.Client, symbol string) (*OpenedPosition, error) {
	var position string

	res, err := bc.NewGetAccountService().Do(ctx)
	if err != nil {
		return &OpenedPosition{}, err
	}

	b, _ := json.Marshal(res.Positions)
	df, _ := imports.LoadFromJSON(ctx, bytes.NewReader(b))

	f, _ := dataframe.Filter(ctx, df, func(vals map[any]any, row int, nRows int) (dataframe.FilterAction, error) {
		if vals["symbol"] != symbol {
			return dataframe.DROP, nil
		}
		return dataframe.KEEP, nil
	})

	positionAmtIdx := df.MustNameToColumn("positionAmt")
	amount, _ := strconv.ParseFloat(f.(*dataframe.DataFrame).Series[positionAmtIdx].Value(0).(string), 64)

	leverageIdx := df.MustNameToColumn("leverage")
	leverage, _ := strconv.ParseFloat(f.(*dataframe.DataFrame).Series[leverageIdx].Value(0).(string), 64)

	entryPriceIdx := df.MustNameToColumn("entryPrice")
	entryPrice, _ := strconv.ParseFloat(f.(*dataframe.DataFrame).Series[entryPriceIdx].Value(0).(string), 64)

	profit, _ := strconv.ParseFloat(res.TotalUnrealizedProfit, 64)

	balance, _ := strconv.ParseFloat(res.TotalWalletBalance, 64)

	if amount > 0 {
		position = string(LONG)
	} else if amount < 0 {
		position = string(SHORT)
	}

	return &OpenedPosition{
		Position:   position,
		Amount:     amount,
		Profit:     profit,
		Leverage:   leverage,
		Balance:    balance,
		EntryPrice: entryPrice,
		Other:      0,
	}, nil
}

// binanceCheckAndCloseOrders проверяет все открытые ордера и закрывает их
func binanceCheckAndCloseOrders(ctx context.Context, bc *futures.Client, symbol string) (bool, error) {
	isStop := true
	orders, _ := bc.NewListOpenOrdersService().
		Symbol(symbol).
		Do(ctx)

	if len(orders) > 0 {
		isStop = false
		err := bc.NewCancelAllOpenOrdersService().Symbol(symbol).Do(ctx)
		if err != nil {
			return isStop, err
		}
	}

	return isStop, nil
}

func binanceCryptoPairPrice(ctx context.Context, bc *futures.Client, symbol string) (price float64) {
	prices, _ := bc.NewListPricesService().Symbol(symbol).Do(ctx)
	for _, p := range prices {
		if p.Symbol == symbol {
			price, _ = strconv.ParseFloat(p.Price, 64)
			break
		}
	}
	return
}
