package utils

import (
    "fmt"
    "time"
)

type Spinner struct {
    stopChan chan bool
    active   bool
    message  string
}

func NewSpinner(message string) *Spinner {
    s := &Spinner{
        stopChan: make(chan bool),
        active:   true,
        message:  message,
    }
    go func() {
        dots := []string{".  ", ".. ", "..."}
        i := 0
        for {
            select {
            case <-s.stopChan:
                fmt.Printf("\r%s\n", message)
                s.active = false
                return
            default:
                fmt.Printf("\r%s%s", message, dots[i%len(dots)])
                i++
                time.Sleep(300 * time.Millisecond)
            }
        }
    }()
    return s
}

func (s *Spinner) Stop() {
    if s.active {
        s.stopChan <- true
    }
}

const (
    ColorReset  = "\033[0m"
    ColorRed    = "\033[31m"
    ColorGreen  = "\033[32m"
    ColorYellow = "\033[33m"
    ColorCyan   = "\033[36m"
)

func Colorize(color, msg string) string {
    return color + msg + ColorReset
}