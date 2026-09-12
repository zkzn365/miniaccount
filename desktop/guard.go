package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// recoverTo 把绑定方法里的 panic 转成 FaultInternal，写进具名返回值。
//
// # 为什么绑定层必须兜住 panic
//
// Wails 的绑定是一次跨进程边界的反射调用。方法里一旦 panic：
//
//   - 前端只拿到一个 rejected promise，错误文本是
//     "runtime error: invalid memory address or nil pointer dereference"
//   - **没有栈**，用户和开发者都不知道是哪条数据触发的
//   - 窗口还开着，看起来像「点了没反应」
//
// 对记账软件尤其糟：用户会以为是自己操作错了，反复重试，
// 而真正的 bug 一点线索都不留。
//
// 转成 FaultInternal 并带上完整栈之后，「复制诊断信息」就能定位问题。
//
// 注意：这不是吞掉 panic —— 它依然写进 stderr，
// 只是去处从「丢失」变成「用户看得见的错误面板」。
//
// 用法（所有绑定方法统一这个形状）：
//
//	func (a *App) Xxx(...) (out T, err error) {
//		defer recoverTo(&err, "Xxx")()
//		...
//	}
//
// 必须用**具名返回值**：函数已经 panic 中断，执行不到 return，
// defer 里改具名返回值是 Go 里唯一能「从 panic 中恢复出一份返回值」的办法。
func recoverTo(errp *error, op string) func() {
	return func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			fmt.Fprintf(os.Stderr, "[panic] %s: %v\n%s\n", op, r, stack)
			f := &Fault{
				Kind:    FaultInternal,
				Message: fmt.Sprintf("%s 发生内部错误：%v", op, r),
				Detail:  string(stack),
			}
			*errp = f
			auditFailure(op, f)
			return
		}
		// ★ 失败也要留痕。
		//
		// 用户报「点了没反应」「报了个错」时，日志里如果只有成功的操作，
		// 就什么都查不到 —— 而失败恰恰最需要线索。
		//
		// 这里只记**失败**：成功的读操作量大又没有业务含义，
		// 全记下来会把真正有用的业务日志淹掉。成功的**写**操作
		// 由 service 层各自记录（还带修改前后的内容）。
		//
		// 注意错误一定是包装过的 Fault（所有绑定都走 classify/wrap），
		// 但不是 Fault 的也要记 —— 那是「有绑定没按规矩包装」的信号，
		// 恰恰更该出现在日志里。
		if *errp != nil {
			if f, ok := (*errp).(*Fault); ok {
				auditFailure(op, f)
			} else {
				auditFailure(op, &Fault{
					Kind: FaultInternal, Message: (*errp).Error(),
					Detail: "这个错误不是 Fault（该绑定没有按约定包装错误）",
				})
			}
		}
	}
}

// recoverValue 兜住**没有 error 返回值**的绑定的 panic。
//
// Wails 绑定的是 *App 的全部导出方法，任何一个 panic 都会把整个应用带崩。
// 有几个绑定（Today、AppVersion）签名上就没有 error，套不上 recoverTo，
// 于是成了没有护栏的缺口 —— 现在它们没有 panic 风险，但「现在没有」
// 不等于「以后没有」，而崩掉的是用户正在记账的程序。
//
// 代价是只能返回零值（调用方拿到空串）。这仍然远好于崩溃：
// 界面拿到空日期会显示为空，用户立刻看得见；崩了则是一个报错窗口
// 加一次未保存的数据丢失。
func recoverValue(op string) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		fmt.Fprintf(os.Stderr, "[panic] %s: %v\n%s\n", op, r, stack)
	}
}

// nonNil 把 nil 切片换成空切片。
//
// ★ 为什么值得一个专门的函数
//
// Go 编 JSON 时，nil 切片是 `null`，空切片才是 `[]`。而界面把
// 「列表字段」当数组用（`data.rows.length`），拿到 null 直接抛异常。
//
// 要命的是它**只在没有数据时出现**：开发时账套里都是演示数据，
// 每个科目都有分录，怎么点都不会崩；用户新建一个空账套点开明细账，
// 立刻炸。用户报的那条就是这样：
//
//	null is not an object (evaluating 'o.value.rows.length')
//
// 所以在 JSON 边界上把 nil 一律换掉。宁可多这一层，
// 也不要让「有没有数据」决定界面崩不崩。
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
