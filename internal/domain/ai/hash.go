package ai

import (
	"crypto/sha256"
	"encoding/hex"
)

// shortHash 返回内容的 sha256 前 8 字节（16 位十六进制）。
//
// 用 sha256 而不是 FNV/CRC：指纹会写进审计表并与用户可见的
// 「提议编号」对应，需要抗碰撞 —— 两条不同的提议撞成同一个编号，
// 事后追溯就断了。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// Digest 返回内容的完整 sha256（64 位十六进制）。
//
// 用于 prompt 指纹：**只存指纹不存原文**，
// 这样既能判断「是不是同一个问题又问了一遍」，
// 又不会把银行流水摘要、对方户名等敏感数据留进数据库。
func Digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
