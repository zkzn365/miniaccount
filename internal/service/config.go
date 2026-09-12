package service

import "miniaccount/internal/domain/calendar"

// dateT 是纯日历日期的别名，只在本包内部使用。
type dateT = calendar.Date

func parseDate(s string) (dateT, error) { return calendar.Parse(s) }

// todayDate 返回今天的纯日历日期。
func todayDate() dateT { return calendar.Today() }
