package main

import (
	"time"

	"gorm.io/gorm"
)

// getSim 取某电表的模拟器状态（懒初始化；序号从 1 开始）
func (s *Server) getSim(code string) *simState {
	st, ok := s.sim[code]
	if !ok {
		st = &simState{NextSeq: 1, Interval: 5 * time.Second, StepKWh: 0.2}
		s.sim[code] = st
	}
	return st
}

// syncSim 登记/换泊位等手工设定表码后，把模拟器对齐到该表码
func (s *Server) syncSim(code string, value float64, at time.Time) {
	if code == "" {
		return
	}
	st := s.getSim(code)
	st.mu.Lock()
	st.Value = value
	st.LastAt = at
	st.mu.Unlock()
}

// advanceSim 设备自动推进：返回新表码（不在这里分配序列号，序列号由 nextSeq 给出）
func (s *Server) advanceSim(code string, at time.Time) float64 {
	st := s.getSim(code)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.LastAt.IsZero() {
		st.LastAt = at
	}
	st.Value += st.StepKWh
	st.LastAt = at
	return st.Value
}

// nextSeq 分配并自增设备序列号
func (s *Server) nextSeq(code string) int {
	st := s.getSim(code)
	st.mu.Lock()
	defer st.mu.Unlock()
	n := st.NextSeq
	st.NextSeq++
	return n
}

// startSimTicker 后台模拟设备：开启 auto 的表按间隔自动回传读数
func (s *Server) startSimTicker() {
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.tickOnce()
		}
	}()
}

func (s *Server) tickOnce() {
	for code, st := range s.sim {
		st.mu.Lock()
		on := st.AutoOn
		interval := st.Interval
		last := st.LastAt
		st.mu.Unlock()
		if !on {
			continue
		}
		now := time.Now()
		if !last.IsZero() && now.Sub(last) < interval {
			continue
		}
		val := s.advanceSim(code, now)
		seq := s.nextSeq(code)
		// 设备回传失败不影响模拟器本身，只打印结果
		_, dup, err := ingestReading(s.DB, readingInput{
			MeterCode: code, Reading: val, ReadAt: now,
			Source: SourceDevice, DeviceSeq: &seq, ReportedBy: "simulator-auto",
		})
		_ = dup
		_ = err
	}
}

var _ = gorm.ErrRecordNotFound
