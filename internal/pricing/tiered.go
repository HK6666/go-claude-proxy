package pricing

// 价格单位常量
const (
	// MicroUSDPerUSD 1美元 = 1,000,000 微美元
	MicroUSDPerUSD = 1_000_000
	// TokensPerMillion 百万tokens
	TokensPerMillion = 1_000_000
)

// CalculateTieredCostMicro 计算分层定价成本（整数运算）
// tokens: token数量
// basePriceMicro: 基础价格 (microUSD/M tokens)
// premiumNum, premiumDenom: 超阈值倍率（分数表示，如 2.0 = 2/1, 1.5 = 3/2）
// threshold: 阈值 token 数
// 返回: 微美元成本
func CalculateTieredCostMicro(tokens uint64, basePriceMicro uint64, premiumNum, premiumDenom, threshold uint64) uint64 {
	if tokens <= threshold {
		return tokens * basePriceMicro / TokensPerMillion
	}
	baseCost := threshold * basePriceMicro / TokensPerMillion
	premiumTokens := tokens - threshold
	// premiumCost = premiumTokens * basePriceMicro * (premiumNum/premiumDenom) / TokensPerMillion
	// 重排以避免溢出: (premiumTokens * basePriceMicro / TokensPerMillion) * premiumNum / premiumDenom
	premiumCost := premiumTokens * basePriceMicro / TokensPerMillion * premiumNum / premiumDenom
	return baseCost + premiumCost
}

// CalculateLinearCostMicro 计算线性定价成本（整数运算）
// tokens: token数量
// priceMicro: 价格 (microUSD/M tokens)
// 返回: 微美元成本
func CalculateLinearCostMicro(tokens, priceMicro uint64) uint64 {
	return tokens * priceMicro / TokensPerMillion
}

// MicroToUSD 将微美元转换为美元（用于显示）
func MicroToUSD(microUSD uint64) float64 {
	return float64(microUSD) / MicroUSDPerUSD
}
