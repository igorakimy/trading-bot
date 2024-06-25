package main

import (
	"fmt"
	"github.com/spf13/viper"
)

type Config struct {
	BotID             string  `mapstructure:"botId"`
	BinanceAPIKey     string  `mapstructure:"binanceApiKey"`
	BinanceAPISecret  string  `mapstructure:"binanceApiSecret"`
	Symbol            string  `mapstructure:"symbol"`
	Interval          string  `mapstructure:"interval"`
	MaxPositionAmount float64 `mapstructure:"maxPositionAmount"`
	StopPercent       float64 `mapstructure:"stopPercent"`
	KlinesCsvFile     string  `mapstructure:"klinesCsvFile"`
}

func MustLoadConfig(filename string) *Config {
	viper.SetConfigName(filename)
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	if err := viper.ReadInConfig(); err != nil {
		panic(fmt.Errorf("fatal error config file: %w", err))
	}

	var C Config
	if err := viper.Unmarshal(&C); err != nil {
		panic(err)
	}

	return &C
}
