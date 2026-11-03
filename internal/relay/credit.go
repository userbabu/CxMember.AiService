package relay

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

const microPerCredit int64 = 1_000_000

// 钱包与单价以整数 microcredit 持久化，对外只输出精确的 credit 十进制字符串。
func formatCredit(amount int64) string {
	return formatCreditAmount(big.NewInt(amount))
}

// 累计充值与消费可能超过单钱包的 Int64 范围，仍以精确十进制文本输出。
func formatCreditAmount(amount *big.Int) string {
	n := new(big.Int).Set(amount)
	sign := ""
	if n.Sign() < 0 {
		sign = "-"
		n.Abs(n)
	}
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(n, big.NewInt(microPerCredit), remainder)
	if remainder.Sign() == 0 {
		return sign + whole.String()
	}
	fraction := strings.TrimRight(fmt.Sprintf("%06d", remainder.Int64()), "0")
	return sign + whole.String() + "." + fraction
}

// 中间乘法使用任意精度整数，小额按 1 microcredit 向上取整，超出钱包范围直接失败。
func price(in, out, inputRate, outputRate int64, multiplier uint64) (int64, error) {
	if in < 0 || out < 0 || inputRate < 0 || outputRate < 0 || multiplier == 0 {
		return 0, errors.New("invalid credit charge inputs")
	}
	weighted := new(big.Int).Mul(big.NewInt(in), big.NewInt(inputRate))
	weighted.Add(weighted, new(big.Int).Mul(big.NewInt(out), big.NewInt(outputRate)))
	weighted.Mul(weighted, new(big.Int).SetUint64(multiplier))
	denominator := big.NewInt(1000 * 10000)
	weighted.Add(weighted, new(big.Int).Sub(new(big.Int).Set(denominator), big.NewInt(1)))
	weighted.Quo(weighted, denominator)
	if !weighted.IsInt64() {
		return 0, errors.New("credit charge exceeds wallet range")
	}
	return weighted.Int64(), nil
}

func priceWithCache(prompt, cached, completion, inputRate, cacheRate, outputRate int64, multiplier uint64) (int64, error) {
	if prompt < 0 || cached < 0 || cached > prompt || completion < 0 || inputRate < 0 || cacheRate < 0 || outputRate < 0 || multiplier == 0 {
		return 0, errors.New("invalid credit charge inputs")
	}
	weighted := new(big.Int).Mul(big.NewInt(prompt-cached), big.NewInt(inputRate))
	weighted.Add(weighted, new(big.Int).Mul(big.NewInt(cached), big.NewInt(cacheRate)))
	weighted.Add(weighted, new(big.Int).Mul(big.NewInt(completion), big.NewInt(outputRate)))
	weighted.Mul(weighted, new(big.Int).SetUint64(multiplier))
	denominator := big.NewInt(1000 * 10000)
	weighted.Add(weighted, new(big.Int).Sub(new(big.Int).Set(denominator), big.NewInt(1)))
	weighted.Quo(weighted, denominator)
	if !weighted.IsInt64() {
		return 0, errors.New("credit charge exceeds wallet range")
	}
	return weighted.Int64(), nil
}
