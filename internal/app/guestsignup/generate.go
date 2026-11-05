package guestsignup

import (
	"crypto/rand"
	_ "embed"
	"fmt"
	"math/big"
	"net/netip"
	"strings"
	"sync"

	"github.com/nyaruka/phonenumbers"
)

// 游客默认名的词表：500 个形容词 × 500 个名词，组合成「形容词的名词」。一行一个词。
var (
	//go:embed words/adjectives.txt
	adjectivesText string
	//go:embed words/nouns.txt
	nounsText string

	adjectives = splitWords(adjectivesText)
	nouns      = splitWords(nounsText)
)

func splitWords(text string) []string {
	var words []string
	for _, line := range strings.Split(text, "\n") {
		if w := strings.TrimSpace(line); w != "" {
			words = append(words, w)
		}
	}
	return words
}

// RandomName 返回一个随机的「形容词的名词」游客名，例如「快乐的熊猫」。
func RandomName() (string, error) {
	a, err := randomIndex(len(adjectives))
	if err != nil {
		return "", err
	}
	n, err := randomIndex(len(nouns))
	if err != nil {
		return "", err
	}
	return adjectives[a] + "的" + nouns[n], nil
}

// 游客号码只取北美的虚构号段 +1 NXX 555 01XX：这一段保留给影视作品等虚构用途，不会分配给
// 真实用户。区号只取号码库（libphonenumber）认为有效的那些，保证在严格号码格式校验
// （TELESRV 的 phone format check）下也是合法号码。号码池约 447 × 100 个。
var (
	guestAreaCodesOnce sync.Once
	guestAreaCodes     []string
)

func validGuestAreaCodes() []string {
	guestAreaCodesOnce.Do(func() {
		for code := 200; code <= 999; code++ {
			number, err := phonenumbers.Parse(fmt.Sprintf("+1%d5550100", code), phonenumbers.UNKNOWN_REGION)
			if err != nil || !phonenumbers.IsValidNumber(number) {
				continue
			}
			guestAreaCodes = append(guestAreaCodes, fmt.Sprintf("%d", code))
		}
	})
	return guestAreaCodes
}

// GuestNumberPoolSize 返回游客号码池的大小。
func GuestNumberPoolSize() int {
	return len(validGuestAreaCodes()) * 100
}

// IsGuestNumber 报告规范化号码（不带 +）是否在游客号段里。
func IsGuestNumber(phone string) bool {
	if len(phone) != 11 || !strings.HasPrefix(phone, "1") || phone[4:9] != "55501" {
		return false
	}
	for _, c := range phone {
		if c < '0' || c > '9' {
			return false
		}
	}
	area := phone[1:4]
	for _, code := range validGuestAreaCodes() {
		if code == area {
			return true
		}
	}
	return false
}

// RandomNumber 返回游客号段里的一个随机号码（规范化形式，不带 +），不检查是否已被占用。
func RandomNumber() (string, error) {
	codes := validGuestAreaCodes()
	if len(codes) == 0 {
		return "", fmt.Errorf("guestsignup: no valid guest area code")
	}
	a, err := randomIndex(len(codes))
	if err != nil {
		return "", err
	}
	line, err := randomIndex(100)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("1%s55501%02d", codes[a], line), nil
}

func randomIndex(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("guestsignup: empty choice")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, fmt.Errorf("guestsignup: rand: %w", err)
	}
	return int(v.Int64()), nil
}

// IPKey 把客户端 IP 规范化成绑定用的键：IPv4（含 IPv4 映射的 IPv6）取地址本身，IPv6 取所在的
// /64 前缀——同一台设备换一个 IPv6 地址不应得到新号。解析不了返回 ok=false。
func IPKey(ip string) (string, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", false
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String(), true
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return "", false
	}
	return prefix.String(), true
}
