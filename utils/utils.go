package utils

import (
	"bufio"
	"io"
	"log"
)

func LogProcess(stderr io.Reader, prefix string) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		log.Printf("[%s] %s", prefix, scanner.Text())
	}
}

func LogFFmpeg(ffmpegStderr io.Reader) { LogProcess(ffmpegStderr, "ffmpeg") }

// Parity returns 1 if the number of set bits is odd, else 0
func Parity(n uint16) byte {
	n ^= n >> 8
	n ^= n >> 4
	n ^= n >> 2
	n ^= n >> 1
	return byte(n & 1)
}