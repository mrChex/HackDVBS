# HackDVBS

A DVB-S transmitter for HackRF One, written in Go. Encodes a live webcam or pre-recorded MPEG-TS stream through a full DVB-S pipeline and transmits it over the air.

> **Hardware note:** A HackRF with an external clock oscillator board (e.g. the H4M) is required for stable enough frequency accuracy to decode DVB-S.

---

## Requirements

### Hardware
- HackRF One with an external clock oscillator board (e.g. H4M)
- Transmit antenna appropriate for your chosen frequency
- Webcam (optional — a test stream is included)

### Software
- Go 1.24+
- FFmpeg
- libhackrf (`sudo apt install libhackrf-dev` on Debian/Ubuntu)

---

## Build

```bash
git clone https://github.com/sarahroselives/HackDVBS
cd HackDVBS
go build -o hackdvbs .
```

---

## Usage

```
./hackdvbs [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-freq` | `1250.0` | Transmit frequency in MHz |
| `-gain` | `30` | TX VGA gain in dB (0–47) |
| `-device` | `/dev/video0` | Video capture device |
| `-size` | `640x480` | Capture resolution |
| `-vbitrate` | `700k` | Video bitrate |
| `-abitrate` | `128k` | Audio bitrate |
| `-fps` | `30` | Frames per second |
| `-colorbars` | `false` | Transmit SMPTE colour bars instead of webcam |
| `-file` | _(disabled)_ | Transmit a pre-recorded `.ts` file |

Press **Ctrl+C** to stop.

### Examples

```bash
# Webcam at 1250 MHz
./hackdvbs -freq 1250.0 -gain 47

# SMPTE colour bars (no webcam needed)
./hackdvbs -colorbars -freq 1250.0 -gain 40

# Transmit the included test stream
./hackdvbs -file test_stream.ts -freq 1250.0 -gain 47
```

---

## Receiver Setup

Configure your DVB-S receiver to match:

| Parameter | Value |
|-----------|-------|
| Frequency | Your `-freq` value |
| Symbol Rate | 1000 ksps |
| Modulation | QPSK |
| FEC | 1/2 |

---

## DVB-S Pipeline

```
MPEG-TS → PRBS Scramble → Reed-Solomon RS(204,188) → Convolutional Interleave → Conv. Encode (rate 1/2) → QPSK → RRC Filter → HackRF
```

| Parameter | Value |
|-----------|-------|
| Symbol rate | 1 Msps |
| Sample rate | 2 Msps |
| Modulation | QPSK |
| FEC | 1/2 convolutional |
| Roll-off | 0.35 |
| Interleave depth | 12 |
| Max usable TS bitrate | ~920 kbps |

The QPSK mapping and PRBS sequence are intentionally matched to SDRangel for compatibility.
