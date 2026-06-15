package tui

import tea "github.com/charmbracelet/bubbletea"

// updateView manages stack updates.
type updateView struct{}

func (updateView) Init() tea.Cmd                           { return nil }
func (updateView) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return updateView{}, nil }
func (updateView) View() string                            { return "Update" }

// doctorView runs and displays diagnostic checks.
type doctorView struct{}

func (doctorView) Init() tea.Cmd                           { return nil }
func (doctorView) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return doctorView{}, nil }
func (doctorView) View() string                            { return "Doctor" }

// logsView streams compose service logs.
type logsView struct{}

func (logsView) Init() tea.Cmd                           { return nil }
func (logsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return logsView{}, nil }
func (logsView) View() string                            { return "Logs" }
