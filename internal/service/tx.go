package service

import "miniaccount/internal/store/sqlite"

// nullI64 把可空指针转成 driver 能接受的值。
//
// 服务层少数几处直接写 SQL 的地方需要它；账务相关的写操作
// 一律走 store 里已有的方法，不在服务层拼 SQL ——
// 那会让「事务边界在哪」变得难以追踪。
func nullI64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// boolI 把布尔值写成 SQLite 的 0/1。
func boolI(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sqliteTx 是存储层事务句柄的别名。
//
// 服务层少数几处需要直接开事务（部门档案这类简单实体的增删改），
// 用别名让签名短一些；账务相关的写操作一律走 store 里已有的方法，
// 不在服务层拼 SQL —— 那会让「事务边界在哪」变得难以追踪。
type sqliteTx = sqlite.Tx
