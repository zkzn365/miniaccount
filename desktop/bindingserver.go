package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// 无窗口运行绑定：把 *App 的导出方法变成一条 JSON Lines 服务
// ---------------------------------------------------------------------------
//
// # 为什么需要它
//
// 这个界面曾经一次都没真正跑起来过，而所有测试都是绿的 ——
// 因为没有任何测试把三层接起来：
//
//	Vue 组件  ←→  api.js  ←→  Wails 的 JS 调用约定  ←→  Go 绑定
//
// Go 侧的测试直接调 Go 方法，跳过了中间两层；前端只有 api.js，
// 而它对 Wails 约定的理解是错的（以为返回 [值, 错误] 元组）。
// 两边各自绿着，接缝上全错。
//
// 有了这个服务，就能在没有窗口的环境里跑真实的界面：
// 前端挂上真组件、真 DOM，把它当成 window.go.main.App，
// Go 这边是真账套、真报表数据。接缝错在哪一层都会立刻暴露。
//
// 它同时也是一个排查工具：界面出问题时，可以把界面跑到浏览器里对着看。
//
// 协议：每行一个 JSON
//
//	请求 {"id":1,"name":"Overview","args":[]}
//	成功 {"id":1,"result":{...}}
//	失败 {"id":1,"error":"{\"kind\":\"no_book\",...}"}
//
// ★ 失败时 error 里放的是 `err.Error()` 的**字符串**，
// 与 Wails 的行为逐字一致（见 wails 的 dispatcher.processCallMessage）。
// 这一点必须模仿，否则测试会测出一个真实运行时里不存在的约定。

// bindingServer 把 App 的方法暴露成 JSON Lines 服务。
type bindingServer struct {
	app *App
}

// request 是一条调用请求。
type request struct {
	ID   int               `json:"id"`
	Name string            `json:"name"`
	Args []json.RawMessage `json:"args"`
}

// response 是一条结果。
type response struct {
	ID     int    `json:"id"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// runBindingServer 从 r 读请求、往 w 写结果，直到 r 结束。
//
// ★ 刻意**不导出**：它是基础设施，不是界面绑定。
// 导出的话 Wails 会把它也绑给前端，而它没有 panic 护栏、
// 也不该被界面调用（wiring_test 会立刻拦下来）。
func (a *App) runBindingServer(r io.Reader, w io.Writer) error {
	srv := &bindingServer{app: a}
	sc := bufio.NewScanner(r)
	// 请求可能很长（凭证带附件、报表几十行），默认 64KB 不够
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	enc := json.NewEncoder(w)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(response{Error: "请求不是合法 JSON：" + err.Error()})
			continue
		}
		result, err := srv.call(req)
		if err != nil {
			// 与 Wails 一致：只把 err.Error() 的字符串透出去
			_ = enc.Encode(response{ID: req.ID, Error: err.Error()})
			continue
		}
		_ = enc.Encode(response{ID: req.ID, Result: result})
	}
	return sc.Err()
}

func (s *bindingServer) call(req request) (any, error) {
	m := reflect.ValueOf(s.app).MethodByName(req.Name)
	if !m.IsValid() {
		return nil, fmt.Errorf("方法 %s 不存在", req.Name)
	}
	mt := m.Type()
	// 绑定的形状固定是 (结果, *Fault)
	if mt.NumIn() != len(req.Args) {
		return nil, fmt.Errorf("%s 需要 %d 个参数，收到 %d 个",
			req.Name, mt.NumIn(), len(req.Args))
	}
	in := make([]reflect.Value, mt.NumIn())
	for i := range req.Args {
		arg := reflect.New(mt.In(i))
		if len(req.Args[i]) > 0 {
			if err := json.Unmarshal(req.Args[i], arg.Interface()); err != nil {
				return nil, fmt.Errorf("%s 的第 %d 个参数无法解析：%w",
					req.Name, i+1, err)
			}
		}
		in[i] = arg.Elem()
	}

	out := m.Call(in)
	if len(out) == 0 {
		return nil, nil
	}
	// 最后一个返回值是 error
	if n := len(out); n > 1 {
		if e, ok := out[n-1].Interface().(error); ok && !isNilErr(e) {
			return nil, e
		}
	}
	return out[0].Interface(), nil
}

// isNilErr 判断「接口里装着一个 nil 指针」这种情况。
//
// Go 里 (T, *Fault) 的第二个返回值在成功时是一个 **nil 的 *Fault**，
// 它装进 error 接口后接口本身不是 nil —— 直接 `err != nil` 会成立，
// 于是每一次成功都被当成失败。Wails 自己也踩这个坑（boundMethod.go 里有注释），
// 这里用反射把「类型非 nil、值 nil」识别出来。
func isNilErr(err error) bool {
	if err == nil {
		return true
	}
	v := reflect.ValueOf(err)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map,
		reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// bindingNames 返回可调用的绑定名，便于排查「名字对不上」。
func (a *App) bindingNames() []string {
	t := reflect.TypeOf(a)
	out := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		out = append(out, t.Method(i).Name)
	}
	sort.Strings(out)
	return out
}

// shutdownHeadless 释放账套连接，供无窗口模式在结束时调用。
//
// 与 shutdown 的区别只是不需要 context —— 无窗口模式没有 Wails 传进来的那个。
// SQLite 的 WAL 要在正常关闭时合并，所以结束时必须调它，
// 否则留下一个「下次打开需要恢复」的库文件。
func (a *App) shutdownHeadless() {
	a.shutdown(nil)
}
