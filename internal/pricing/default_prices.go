package pricing

import "sync"

var (
	defaultTable *PriceTable
	defaultOnce  sync.Once
)

// DefaultPriceTable 返回默认价格表（单例）
func DefaultPriceTable() *PriceTable {
	defaultOnce.Do(func() {
		defaultTable = initDefaultPrices()
	})
	return defaultTable
}

// initDefaultPrices 初始化默认价格
func initDefaultPrices() *PriceTable {
	pt := NewPriceTable("2025.01")

	// ========== Claude 4.5 系列 ==========
	// Claude Sonnet 4.5: input=$3, output=$15, cache_creation=$3.75, cache_read=$0.30
	pt.Set(&ModelPricing{
		ModelID:                "claude-sonnet-4-5",
		InputPriceMicro:        3_000_000,  // $3.00/M
		OutputPriceMicro:       15_000_000, // $15.00/M
		Cache5mWritePriceMicro: 3_750_000,  // $3.75/M
		Cache1hWritePriceMicro: 3_750_000,  // $3.75/M
		CacheReadPriceMicro:    300_000,    // $0.30/M
		Has1MContext:           true,
	})

	// Claude Opus 4.5: input=$5, output=$25, cache_creation=$6.25, cache_read=$0.50
	pt.Set(&ModelPricing{
		ModelID:                "claude-opus-4-5",
		InputPriceMicro:        5_000_000,  // $5.00/M
		OutputPriceMicro:       25_000_000, // $25.00/M
		Cache5mWritePriceMicro: 6_250_000,  // $6.25/M
		Cache1hWritePriceMicro: 6_250_000,  // $6.25/M
		CacheReadPriceMicro:    500_000,    // $0.50/M
	})

	// Claude Haiku 4.5: input=$1, output=$5, cache_creation=$1.25, cache_read=$0.10
	pt.Set(&ModelPricing{
		ModelID:                "claude-haiku-4-5",
		InputPriceMicro:        1_000_000, // $1.00/M
		OutputPriceMicro:       5_000_000, // $5.00/M
		Cache5mWritePriceMicro: 1_250_000, // $1.25/M
		Cache1hWritePriceMicro: 1_250_000, // $1.25/M
		CacheReadPriceMicro:    100_000,   // $0.10/M
	})

	// ========== GPT 5.x 系列 ==========
	// gpt-5.1: input=$1.25, cache_read=$0.125, output=$10
	pt.Set(&ModelPricing{
		ModelID:             "gpt-5.1",
		InputPriceMicro:     1_250_000,  // $1.25/M
		OutputPriceMicro:    10_000_000, // $10.00/M
		CacheReadPriceMicro: 125_000,    // $0.125/M
	})

	// gpt-5.1-codex: input=$1.25, cache_read=$0.125, output=$10
	pt.Set(&ModelPricing{
		ModelID:             "gpt-5.1-codex",
		InputPriceMicro:     1_250_000,  // $1.25/M
		OutputPriceMicro:    10_000_000, // $10.00/M
		CacheReadPriceMicro: 125_000,    // $0.125/M
	})

	// gpt-5.1-codex-max: input=$1.25, cache_read=$0.125, output=$10
	pt.Set(&ModelPricing{
		ModelID:             "gpt-5.1-codex-max",
		InputPriceMicro:     1_250_000,  // $1.25/M
		OutputPriceMicro:    10_000_000, // $10.00/M
		CacheReadPriceMicro: 125_000,    // $0.125/M
	})

	// gpt-5.2: input=$1.75, cache_read=$0.175, output=$14
	pt.Set(&ModelPricing{
		ModelID:             "gpt-5.2",
		InputPriceMicro:     1_750_000,  // $1.75/M
		OutputPriceMicro:    14_000_000, // $14.00/M
		CacheReadPriceMicro: 175_000,    // $0.175/M
	})

	// gpt-5.2-codex: input=$1.75, cache_read=$0.175, output=$14
	pt.Set(&ModelPricing{
		ModelID:             "gpt-5.2-codex",
		InputPriceMicro:     1_750_000,  // $1.75/M
		OutputPriceMicro:    14_000_000, // $14.00/M
		CacheReadPriceMicro: 175_000,    // $0.175/M
	})

	// ========== Gemini 3.x 系列 ==========
	// gemini-3-pro-preview: input=$2, cache_read=$0.20, output=$12
	pt.Set(&ModelPricing{
		ModelID:             "gemini-3-pro-preview",
		InputPriceMicro:     2_000_000,  // $2.00/M
		OutputPriceMicro:    12_000_000, // $12.00/M
		CacheReadPriceMicro: 200_000,    // $0.20/M
	})

	// gemini-3-flash-preview: input=$0.50, cache_read=$0.05, output=$3
	pt.Set(&ModelPricing{
		ModelID:             "gemini-3-flash-preview",
		InputPriceMicro:     500_000,   // $0.50/M
		OutputPriceMicro:    3_000_000, // $3.00/M
		CacheReadPriceMicro: 50_000,    // $0.05/M
	})

	// ========== Gemini 2.5 系列 ==========
	// gemini-2.5-pro: input=$1.25, cache_read=$0.125, output=$10
	pt.Set(&ModelPricing{
		ModelID:             "gemini-2.5-pro",
		InputPriceMicro:     1_250_000,  // $1.25/M
		OutputPriceMicro:    10_000_000, // $10.00/M
		CacheReadPriceMicro: 125_000,    // $0.125/M
	})

	// gemini-2.5-flash: input=$0.30, cache_read=$0.10, output=$2.50
	pt.Set(&ModelPricing{
		ModelID:             "gemini-2.5-flash",
		InputPriceMicro:     300_000,   // $0.30/M
		OutputPriceMicro:    2_500_000, // $2.50/M
		CacheReadPriceMicro: 100_000,   // $0.10/M
	})

	return pt
}
