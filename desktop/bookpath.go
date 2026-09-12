package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"

	"miniaccount/internal/store/sqlite"
)

// ---------------------------------------------------------------------------
// 账套的默认保存位置
// ---------------------------------------------------------------------------
//
// # 为什么要有默认位置
//
// 建账表单原来要求用户**自己填一个文件路径**，不填就弹「请填写账套文件的
// 保存位置」—— 而第一次打开软件的人根本不知道该填什么：
// 放哪儿？文件名写什么？会不会覆盖别的东西？
//
// 对被要求「会用 Excel 就会用这个」的用户来说，这一栏就是一个死胡同。
// 所以现在给一个默认位置，用户什么都不用改就能建账；
// 想放别处再改。
//
// # 放在哪
//
//	<用户家目录>/.mini-account/dataDB
//
// 三个平台（macOS / Windows / Linux）**完全一致**。
//
//   - 单独一层 dataDB：账套（.db）与附件目录（.files）都放在里面，
//     备份时整个目录拷走即可。
//   - 不放在程序旁边：.app / .exe 可能被拖进 /Applications、
//     被重装覆盖、或者从只读位置运行 —— 那样账套会跟着丢。
//   - 不放在「文档」目录里：那里的名字随系统语言变（中文 Linux 是
//     ~/文档，Windows 上还可能被重定向到 D 盘或被 OneDrive 接管），
//     为了一个目录名去适配三套系统规则，不值得。
//
// ★ 代价要说清楚：以点开头是**隐藏目录**，用户自己翻是翻不到的。
// 所以界面上必须给出路 —— 建账页有「打开目录」按钮，直接调起
// 系统文件管理器定位到这里（见 dialogs.go）。把账套藏起来却不给
// 入口，才是真正的问题；藏起来但一键可达，是可以接受的。
const (
	appFolderName   = ".mini-account"
	bookDataDirName = "dataDB"
	// bookFileExt 是账套文件的扩展名。
	bookFileExt = ".db"
	// defaultBookBaseName 是算不出英文名时的兜底（单位名称留空、
	// 或全是标点之类）。
	defaultBookBaseName = "book"
	// maxBookBaseName 是文件名主干的长度上限（不含 .db）。
	//
	// 60 个 ASCII 字符足够表达一个公司名，也离任何平台的
	// 路径长度限制都还很远。超了就切在音节边界上。
	maxBookBaseName = 60
)

// defaultBookDir 返回账套默认保存目录（不保证已存在）。
func defaultBookDir() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, appFolderName, bookDataDirName), nil
	}
	// 家目录问不到（容器、被裁剪的 CI 环境）时退到本机配置目录。
	// 位置不理想，但比「算不出路径、让用户自己填」好 ——
	// 后者正是这次要修掉的那个死胡同。
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		return filepath.Join(cfg, appFolderName, bookDataDirName), nil
	}
	return "", fmt.Errorf("问不到用户家目录，也问不到配置目录")
}

// DefaultBookDir 返回账套默认保存目录，并确保它存在。
//
// 界面用它显示「账套会存在这里」，也用它给文件名兜底，
// 还给系统的文件选择框当默认位置（目录必须先存在，
// 否则 Wails 会直接报 "default directory does not exist"）。
func (a *App) DefaultBookDir() (out string, err error) {
	defer recoverTo(&err, "DefaultBookDir")()
	dir, derr := defaultBookDir()
	if derr != nil {
		return "", &Fault{Kind: FaultIO, Message: "找不到账套目录：" + derr.Error()}
	}
	if merr := os.MkdirAll(dir, 0o755); merr != nil {
		return "", &Fault{
			Kind:    FaultIO,
			Message: "建不了账套目录 " + dir + "：" + merr.Error(),
		}
	}
	return dir, nil
}

// SuggestBookPath 按单位名称给一个默认账套路径。
//
// ★ 文件名一律是**英文**：中文单位名称转成拼音，
// 「杭州云帆软件有限公司」→ `hangzhou-yunfan-ruanjian-youxian-gongsi.db`。
//
// 为什么不用中文文件名：账套要在三种系统、备份包、U 盘、网盘之间搬，
// 中文名在这些环节里出问题的概率不低（编码、归一化、命令行、
// 别人的脚本），而用户看不出问题出在哪。拼音名同样是可读的，
// 而且 ASCII 到处都认。
//
// 重名时自动加序号（-2、-3…）：单位名称留空时大家都会落到
// `book.db`，不避让的话第二本账会直接盖在第一本上。
func (a *App) SuggestBookPath(companyName string) (out string, err error) {
	defer recoverTo(&err, "SuggestBookPath")()
	dir, f := a.DefaultBookDir()
	if f != nil {
		return "", f
	}
	return uniqueBookPath(dir, bookBaseName(companyName)), nil
}

// bookBaseName 把单位名称变成英文文件名（不含扩展名）。
func bookBaseName(companyName string) string {
	if s := pinyinSlug(companyName); s != "" {
		return s
	}
	return defaultBookBaseName
}

// uniqueBookPath 在 dir 下找一个还没被占用的 <base>.db。
//
// 只加到 -99 就停：再往上多半不是「重名」而是目录被塞满了，
// 继续试下去只会让建账卡住，不如把选择权还给用户。
func uniqueBookPath(dir, base string) string {
	p := filepath.Join(dir, base+bookFileExt)
	for i := 2; i <= 99; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, bookFileExt))
	}
	return p
}

// pinyinSlug 把单位名称转成文件名安全的拼音串。
//
// 规则：
//   - 汉字取拼音（不带声调，全小写），**每个字一个音节**，音节之间用 `-`：
//     「杭州云帆软件有限公司」→ `hang-zhou-yun-fan-ruan-jian-you-xian-gong-si`；
//   - ASCII 字母数字原样保留（转小写），连续的一段算一个词：
//     「ABC 科技」→ `abc-ke-ji`；
//   - 其它字符（空格、标点、括号、路径分隔符……）一律当分隔符；
//   - 连续分隔符压成一个 `-`，首尾的 `-` 去掉。
//
// ★ 只保留 ASCII 是**安全**上的要求，不只是好看：
// 路径分隔符、Windows 保留字符、以及中文里的全角标点，
// 一旦原样进了文件名，轻则建不出文件，重则把账套建到别的目录去。
//
// 汉字按字切音节、不按词切：中文分词要另挂一套词典与算法，
// 而文件名只要稳定、可读、能区分就够了。
func pinyinSlug(s string) string {
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal

	var b strings.Builder
	pendingSep := false

	// put 追加一个词；上一个字符是分隔符时先在中间补 `-`。
	put := func(word string) {
		if b.Len() > 0 && pendingSep {
			b.WriteByte('-')
		}
		b.WriteString(word)
		pendingSep = false
	}
	// sep 记下「这里有一个分隔」；开头的分隔会被忽略。
	sep := func() {
		if b.Len() > 0 {
			pendingSep = true
		}
	}

	for _, r := range strings.TrimSpace(s) {
		switch {
		case r < unicode.MaxASCII && (isASCIILetter(r) || unicode.IsDigit(r)):
			put(strings.ToLower(string(r)))

		case unicode.Is(unicode.Han, r):
			// 多音字按最常用的读音取第一个 —— 文件名只要**稳定**
			// 和可读就够了，不追求人名地名那种精确读音。
			if py := pinyin.SinglePinyin(r, args); len(py) > 0 && py[0] != "" {
				sep() // 音节之间要断开，否则是一长串没法读的字母
				put(strings.ToLower(py[0]))
			} else {
				sep()
			}

		default:
			// 拉丁字母带变音符号（é、ü…）也尽量留下可读的部分；
			// 其它一律当分隔符。
			if w := latinFold(r); w != "" {
				put(w)
			} else {
				sep()
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	// 文件名过长会让某些文件系统直接失败，也会撞上路径长度限制。
	// 截断要**切在音节边界上** —— 从 `gong-si` 中间砍成 `gong-s`
	// 只会让人以为文件名被弄坏了。
	if len(out) > maxBookBaseName {
		out = out[:maxBookBaseName]
		if i := strings.LastIndexByte(out, '-'); i > 0 {
			out = out[:i]
		}
		out = strings.Trim(out, "-")
	}
	return out
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// latinFold 把常见的带变音符号的拉丁字母折成 ASCII。
//
// 只覆盖欧洲语言里最常见的那些；覆盖不到的一律当分隔符 ——
// 宁可少一个字母，也不能让一个非 ASCII 字符进文件名。
func latinFold(r rune) string {
	switch r {
	case 'à', 'á', 'â', 'ã', 'ä', 'å', 'ā', 'ă', 'ą':
		return "a"
	case 'ç', 'ć', 'č':
		return "c"
	case 'è', 'é', 'ê', 'ë', 'ē', 'ę':
		return "e"
	case 'ì', 'í', 'î', 'ï', 'ī':
		return "i"
	case 'ñ', 'ń':
		return "n"
	case 'ò', 'ó', 'ô', 'õ', 'ö', 'ø', 'ō':
		return "o"
	case 'ù', 'ú', 'û', 'ü', 'ū':
		return "u"
	case 'ý', 'ÿ':
		return "y"
	case 'ß':
		return "ss"
	case 'æ':
		return "ae"
	}
	return ""
}

// ---------------------------------------------------------------------------
// 启动时先看看账套目录里有什么
// ---------------------------------------------------------------------------

// BookList 是启动时给界面看的「这里有哪几本账」。
type BookList struct {
	// Dir 是默认账套目录（界面上要显示出来）。
	Dir string `json:"dir"`
	// Books 是目录里的账套（已按「最近改动的在前」排好）。
	Books []sqlite.BookPeek `json:"books"`
	// Others 是目录里不是账套的 .db 文件，单独列出来。
	//
	// 不混进 Books 里：用户误放的备份、别的软件的库都可能在这儿，
	// 混在一起会让他以为「我的账套坏了」。
	Others []sqlite.BookPeek `json:"others"`
	// LastPath 是上次打开过的账套（可能已被移走）。
	LastPath string `json:"lastPath"`
	// Suggested 是建议默认打开的那一本。
	//
	// 规则：上次打开的那本 > 最近改动的那本。界面把它预先选中，
	// 用户直接点「打开」就行 —— 默认动作必须是他最可能想要的那一个。
	Suggested string `json:"suggested"`
	// HasAny 为真表示目录里有账套，界面应当先问「打开还是新建」。
	HasAny bool `json:"hasAny"`
}

// ListBooks 扫描账套目录，供启动时选择。
//
// 只读：绝不建表、绝不迁移 —— 目录里可能有用户误放的别的文件。
func (a *App) ListBooks() (out BookList, err error) {
	defer recoverTo(&err, "ListBooks")()

	dir, f := a.DefaultBookDir()
	if f != nil {
		// 目录都建不出来时也要能进建账页，不能把用户堵死
		return BookList{}, nil
	}
	out.Dir = dir

	// 上次用的那本可能在别处（用户自己选的路径），一并扫出来
	last := lastBookPath()
	out.LastPath = last

	books, serr := sqlite.ScanBookDir(a.context(), dir)
	if serr != nil {
		return BookList{}, classify(serr)
	}
	for _, b := range books {
		if b.IsBook {
			out.Books = append(out.Books, b)
		} else {
			out.Others = append(out.Others, b)
		}
	}

	// 上次打开的那本不在默认目录里（用户放在别处）时，也补进来，
	// 否则「上次用的账套」会在列表里凭空消失。
	if last != "" && filepath.Dir(last) != dir {
		if _, statErr := os.Stat(last); statErr == nil {
			p := sqlite.PeekBook(a.context(), last)
			out.Books = append([]sqlite.BookPeek{p}, out.Books...)
		}
	}

	out.HasAny = len(out.Books) > 0
	for _, b := range out.Books {
		if last != "" && b.Path == last {
			out.Suggested = b.Path
			break
		}
	}
	if out.Suggested == "" && len(out.Books) > 0 {
		out.Suggested = out.Books[0].Path
	}
	return out, nil
}
