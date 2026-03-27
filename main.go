package main

import (
    "context"
    "errors"
    "flag"
    "log"
    "os/exec"
    "strconv"
    "strings"
    "time"

    "github.com/samuel/go-hackrf/hackrf"
    "hackdvbs/consts"
    "hackdvbs/dvbs"
    "hackdvbs/filter"
    "hackdvbs/utils"
)

const (
    // Buffer size for streaming mode - back to 2Msps
    streamBufferSize = 8 * 1024 * 1024 // ~4 seconds at 2 Msps
)

func main() {
    freq := flag.Float64("freq", 1250.0, "Transmit frequency in MHz")
    gain := flag.Int("gain", 30, "TX VGA gain (0-47)")
    device := flag.String("device", "/dev/video0", "Video device (Linux) or device index (e.g., '0' for Windows/Mac)")
    videoSize := flag.String("size", "640x480", "Video resolution (e.g., 640x480, 1280x720)")
    videoBitrate := flag.String("vbitrate", "700k", "Video bitrate (e.g., 500k, 700k, 1M)")
    audioBitrate := flag.String("abitrate", "128k", "Audio bitrate (e.g., 64k, 128k)")
    fps := flag.Int("fps", 30, "Frames per second")
    colorBars := flag.Bool("colorbars", false, "Use SMPTE color bars instead of webcam")
    inputFile := flag.String("file", "", "Transmit a pre-recorded .ts file instead of live source")
    libCamera := flag.Bool("libcamera", false, "Use rpicam-vid (libcamera) as video source (Raspberry Pi CM5/Pi 5)")
    flag.Parse()

    log.Println("--- Starting DVB-S Webcam Transmitter ---")
    log.Printf("Frequency: %.2f MHz, Gain: %d dB", *freq, *gain)

    var ffmpegCmd *exec.Cmd
    var rpicamCmd *exec.Cmd
    if *inputFile != "" {
        log.Printf("Source: File (%s)", *inputFile)
        ffmpegCmd = buildFileCommand(*inputFile)
    } else if *colorBars {
        log.Printf("Video: %s @ %d fps, bitrate: %s", *videoSize, *fps, *videoBitrate)
        log.Println("Source: SMPTE Color Bars (test pattern)")
        ffmpegCmd = buildFFmpegCommand(*device, *videoSize, *fps, *videoBitrate, *audioBitrate, true)
    } else if *libCamera {
        log.Printf("Video: %s @ %d fps, bitrate: %s", *videoSize, *fps, *videoBitrate)
        log.Println("Source: libcamera (rpicam-vid)")
        rpicamCmd, ffmpegCmd = buildLibcameraCommands(*videoSize, *fps, *videoBitrate, *audioBitrate)
    } else {
        log.Printf("Video: %s @ %d fps, bitrate: %s", *videoSize, *fps, *videoBitrate)
        log.Printf("Source: Webcam (%s)", *device)
        ffmpegCmd = buildFFmpegCommand(*device, *videoSize, *fps, *videoBitrate, *audioBitrate, false)
    }

    // Start rpicam-vid if using libcamera mode (must start before ffmpeg)
    if rpicamCmd != nil {
        rpicamStderr, err := rpicamCmd.StderrPipe()
        if err != nil {
            log.Fatalf("Failed to get rpicam-vid stderr pipe: %v", err)
        }
        if err := rpicamCmd.Start(); err != nil {
            log.Fatalf("Failed to start rpicam-vid: %v", err)
        }
        defer rpicamCmd.Process.Kill()
        go utils.LogProcess(rpicamStderr, "rpicam-vid")
    }

    // Start FFmpeg to capture webcam and encode to MPEG-TS

    ffmpegStdout, err := ffmpegCmd.StdoutPipe()
    if err != nil {
        log.Fatalf("Failed to get FFmpeg stdout pipe: %v", err)
    }

    ffmpegStderr, err := ffmpegCmd.StderrPipe()
    if err != nil {
        log.Fatalf("Failed to get FFmpeg stderr pipe: %v", err)
    }

    if err := ffmpegCmd.Start(); err != nil {
        log.Fatalf("Failed to start FFmpeg: %v", err)
    }
    defer ffmpegCmd.Process.Kill()

    // Log FFmpeg output in background
    go utils.LogFFmpeg(ffmpegStderr)

    // Initialize HackRF
    if err := hackrf.Init(); err != nil {
        log.Fatalf("hackrf.Init() failed: %v", err)
    }
    defer hackrf.Exit()

    dev, err := hackrf.Open()
    if err != nil {
        log.Fatalf("hackrf.Open() failed: %v", err)
    }
    defer dev.Close()

    dev.SetFreq(uint64(*freq * 1_000_000))
    dev.SetSampleRate(consts.HackRFSampleRate)
    dev.SetTXVGAGain(*gain)
    dev.SetAmpEnable(true)  // Re-enable amp
    dev.SetBasebandFilterBandwidth(1750000)

    // Create DVB-S encoder and filter
    rrcFilter := filter.NewRRCFilter(consts.SymbolRate, consts.HackRFSampleRate, consts.RollOffFactor, consts.RRCFilterTaps)
    dvbsEncoder := dvbs.NewDVBSEncoder()

    // Create I/Q sample buffer and channel - use complex64 for speed
    iqChannel := make(chan complex64, 2*1024*1024)
    sampleBuffer := make([]complex64, streamBufferSize)
    bufferReadPos := 0
    bufferWritePos := 0

    // Start the DVB-S encoding goroutine
    go dvbs.StreamToIQ(ffmpegStdout, iqChannel, dvbsEncoder, rrcFilter)

    // Wait for channel to fill substantially before buffering
    log.Println("Waiting for encoder to build up data...")
    targetChannelFill := 1500000 // ~0.75 seconds at 2Msps
    for {
        channelSize := len(iqChannel)
        if channelSize >= targetChannelFill {
            log.Printf("Channel ready with %d samples", channelSize)
            break
        }
        log.Printf("Channel filling... %d / %d samples (%.1f%%)", channelSize, targetChannelFill, float64(channelSize)*100/float64(targetChannelFill))
        time.Sleep(1 * time.Second)
    }
    
    // Pre-fill buffer
    log.Println("Pre-filling buffer...")
    for i := 0; i < streamBufferSize; i++ {
        sample, ok := <-iqChannel
        if !ok {
            log.Fatal("Stream ended before buffer was filled")
        }
        sampleBuffer[i] = sample
    }
    bufferWritePos = 0
    
    // Final check - channel should still have plenty
    channelFill := len(iqChannel)
    log.Printf("Buffer filled (%d samples = %.2f seconds), channel has %d samples ready", 
        streamBufferSize, float64(streamBufferSize)/float64(consts.HackRFSampleRate), channelFill)
    
    // Don't start until we have reserve
    for channelFill < 200000 {
        log.Printf("Waiting for reserve... channel at %d samples", channelFill)
        time.Sleep(2 * time.Second)
        channelFill = len(iqChannel)
    }
    
    log.Println("Starting transmission...")

    // Track buffer health
    var bufferUnderflows uint64

    // Background goroutine to continuously fill the buffer
    go func() {
        for sample := range iqChannel {
            sampleBuffer[bufferWritePos] = sample
            bufferWritePos = (bufferWritePos + 1) % streamBufferSize
        }
        log.Println("Warning: IQ channel closed, no more samples!")
    }()

    // Buffer health monitoring
    go func() {
        ticker := time.NewTicker(5 * time.Second)
        defer ticker.Stop()
        for range ticker.C {
            available := (bufferWritePos - bufferReadPos + streamBufferSize) % streamBufferSize
            fillPct := float64(available) * 100.0 / float64(streamBufferSize)
            log.Printf("Buffer: %.1f%% full (%d samples), underflows: %d", fillPct, available, bufferUnderflows)
            if fillPct < 10 {
                log.Printf("WARNING: Buffer critically low!")
            }
        }
    }()

    // Start transmission
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    const digitalGain = 100.0  // Compromise between clipping and power

    err = dev.StartTX(func(buf []byte) error {
        select {
        case <-ctx.Done():
            return errors.New("transfer cancelled")
        default:
        }

        samplesToWrite := len(buf) / 2
        for i := 0; i < samplesToWrite; i++ {
            // Check if we're about to overtake the write position
            if bufferReadPos == bufferWritePos {
                bufferUnderflows++
                // Hold last sample on underflow instead of wrapping
                sample := sampleBuffer[(bufferReadPos-1+streamBufferSize)%streamBufferSize]
                i_sample := int8(real(sample) * digitalGain)
                q_sample := int8(imag(sample) * digitalGain)
                buf[i*2] = byte(i_sample)
                buf[i*2+1] = byte(q_sample)
                continue
            }
            
            sample := sampleBuffer[bufferReadPos]
            i_sample := int8(real(sample) * digitalGain)
            q_sample := int8(imag(sample) * digitalGain)
            buf[i*2] = byte(i_sample)
            buf[i*2+1] = byte(q_sample)

            bufferReadPos = (bufferReadPos + 1) % streamBufferSize
        }
        return nil
    })

    if err != nil {
        if err.Error() != "transfer cancelled" {
            log.Fatalf("StartTX failed: %v", err)
        }
    }

    log.Println("Transmission is live. Press Ctrl+C to stop.")
    utils.WaitForSignal()

    log.Println("Stopping transmission...")
    cancel()
    dev.StopTX()
    ffmpegCmd.Process.Kill()
    log.Println("Transmission stopped.")
}

func buildFFmpegCommand(device, videoSize string, fps int, videoBitrate, audioBitrate string, colorBars bool) *exec.Cmd {
    if colorBars {
        // Use test pattern (SMPTE color bars)
        args := []string{
            "-f", "lavfi",
            "-i", "smptebars=size=" + videoSize + ":rate=" + strconv.Itoa(fps),
            "-f", "lavfi",
            "-i", "sine=frequency=1000:sample_rate=48000",
            "-c:v", "mpeg2video",
            "-pix_fmt", "yuv420p",
            "-b:v", videoBitrate,
            "-maxrate", videoBitrate,
            "-bufsize", "1400k",
            "-g", "10",
            "-bf", "0",
            "-c:a", "mp2",
            "-b:a", audioBitrate,
            "-ar", "44100",
            "-f", "mpegts",
            "-muxrate", "1M",
            "-pcr_period", "20",
            "-",
        }
        return exec.Command("ffmpeg", args...)
    }

    // Webcam: Settings matching working leandvbtx pipeline
    args := []string{
        "-thread_queue_size", "512",
        "-f", "v4l2",
        "-video_size", videoSize,
        "-framerate", strconv.Itoa(fps),
        "-i", device,
        "-thread_queue_size", "512",
        "-f", "alsa",
        "-i", "default",
        "-r", strconv.Itoa(fps), // Force output framerate
        "-c:v", "mpeg2video",
        "-pix_fmt", "yuv420p",
        "-b:v", videoBitrate,
        "-maxrate", videoBitrate,
        "-bufsize", "1400k",
        "-g", "10",
        "-bf", "0",
        "-c:a", "mp2",
        "-b:a", audioBitrate,
        "-ar", "44100",
        "-f", "mpegts",
        "-muxrate", "1M",
        "-pcr_period", "20",
        "-",
    }
    return exec.Command("ffmpeg", args...)
}

func buildLibcameraCommands(videoSize string, fps int, videoBitrate, audioBitrate string) (*exec.Cmd, *exec.Cmd) {
    parts := strings.SplitN(videoSize, "x", 2)
    width, height := parts[0], parts[1]

    rpicamCmd := exec.Command("rpicam-vid",
        "--width", width,
        "--height", height,
        "--framerate", strconv.Itoa(fps),
        "--nopreview",
        "--timeout", "0",
        "--codec", "yuv420",
        "-o", "-",
    )

    ffmpegCmd := exec.Command("ffmpeg",
        "-f", "rawvideo",
        "-pix_fmt", "yuv420p",
        "-s", videoSize,
        "-r", strconv.Itoa(fps),
        "-i", "pipe:0",
        "-f", "lavfi",
        "-i", "aevalsrc=0:c=stereo:s=44100",
        "-c:v", "mpeg2video",
        "-pix_fmt", "yuv420p",
        "-b:v", videoBitrate,
        "-maxrate", videoBitrate,
        "-bufsize", "1400k",
        "-g", "10",
        "-bf", "0",
        "-c:a", "mp2",
        "-b:a", audioBitrate,
        "-ar", "44100",
        "-f", "mpegts",
        "-muxrate", "1M",
        "-pcr_period", "20",
        "-",
    )

    rpicamStdout, _ := rpicamCmd.StdoutPipe()
    ffmpegCmd.Stdin = rpicamStdout

    return rpicamCmd, ffmpegCmd
}

func buildFileCommand(filename string) *exec.Cmd {
    // Stream pre-recorded .ts file - no rate limiting, let buffer handle it
    args := []string{
        "-stream_loop", "-1", // Loop forever
        "-i", filename,
        "-c", "copy", // No re-encoding
        "-f", "mpegts",
        "-",
    }
    return exec.Command("ffmpeg", args...)
}
