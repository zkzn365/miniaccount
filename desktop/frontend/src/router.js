import { createRouter, createWebHashHistory } from 'vue-router'
import { bookState } from './lib/book'

// 用 hash 模式：Wails 把前端产物内嵌进二进制后由 AssetServer 提供，
// history 模式下刷新任意子路径都会 404。
const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue'), meta: { title: '首页', icon: 'Home' } },
  { path: '/vouchers', name: 'vouchers', component: () => import('./views/Vouchers.vue'), meta: { title: '凭证', icon: 'FileText' } },
  { path: '/invoices', name: 'invoices', component: () => import('./views/Invoices.vue'), meta: { title: '发票', icon: 'Receipt' } },
  { path: '/expenses', name: 'expenses', component: () => import('./views/Expenses.vue'), meta: { title: '报销', icon: 'Plane' } },
  { path: '/bank', name: 'bank', component: () => import('./views/Bank.vue'), meta: { title: '银行流水', icon: 'Landmark' } },
  { path: '/payroll', name: 'payroll', component: () => import('./views/Payroll.vue'), meta: { title: '工资', icon: 'Users' } },
  { path: '/periods', name: 'periods', component: () => import('./views/Periods.vue'), meta: { title: '账期管理', icon: 'CalendarRange' } },
  { path: '/reports', name: 'reports', component: () => import('./views/Reports.vue'), meta: { title: '报表', icon: 'Table2' } },
  { path: '/aging', name: 'aging', component: () => import('./views/Aging.vue'), meta: { title: '账龄分析', icon: 'Hourglass' } },
  { path: '/statements', name: 'statements', component: () => import('./views/Statements.vue'), meta: { title: '对账单', icon: 'FileCheck' } },
  { path: '/recon', name: 'recon', component: () => import('./views/Reconciliation.vue'), meta: { title: '银行余额调节', icon: 'Scale' } },
  { path: '/summary', name: 'summary', component: () => import('./views/Summary.vue'), meta: { title: '凭证汇总表', icon: 'Sigma' } },
  { path: '/columnar', name: 'columnar', component: () => import('./views/Columnar.vue'), meta: { title: '多栏式明细账', icon: 'Columns3' } },
  { path: '/ledger', name: 'ledger', component: () => import('./views/Ledger.vue'), meta: { title: '明细账', icon: 'BookOpen' } },
  { path: '/ai', name: 'ai', component: () => import('./views/AIAssistant.vue'), meta: { title: 'AI 记账助手', icon: 'Sparkles' } },
  { path: '/accounts', name: 'accounts', component: () => import('./views/Accounts.vue'), meta: { title: '科目管理', icon: 'ListTree' } },
  { path: '/auxiliary', name: 'auxiliary', component: () => import('./views/Auxiliary.vue'), meta: { title: '辅助核算', icon: 'Shapes' } },
  { path: '/vat', name: 'vat', component: () => import('./views/VAT.vue'), meta: { title: '增值税', icon: 'Percent' } },
  { path: '/audit', name: 'audit', component: () => import('./views/AuditLog.vue'), meta: { title: '操作日志', icon: 'ScrollText' } },
  { path: '/settings', name: 'settings', component: () => import('./views/Settings.vue'), meta: { title: '设置与备份', icon: 'Settings' } },
  { path: '/welcome', name: 'welcome', component: () => import('./views/Welcome.vue'), meta: { title: '建账', hidden: true } },
]

const router = createRouter({ history: createWebHashHistory(), routes })

// ★ 没有账套就不许进业务页面。
//
// 界面上的菜单已经置灰了，但还有别的入口能走到那些路由：
// 地址栏、书签、浏览器的前进后退、以及代码里的 router.push。
// 每一条都要挡住 —— 否则用户会看到一个「要账套数据却没有账套」的页面，
// 上面除了报错什么都没有。
//
// 启动流程（main.js）在挂载之前已经问过一次账套状态，
// 所以这里读到的 bookState 是准的；ready 之前不拦，
// 免得把「还没问」当成「没有」。
router.beforeEach((to) => {
  if (!bookState.ready || bookState.open) return true
  if (to.name === 'welcome') return true
  return { name: 'welcome' }
})

export default router
