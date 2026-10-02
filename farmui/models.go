package main

import "time"

// AVD describes a virtual device and, when running, its live state.
type AVD struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Userdata  string   `json:"userdata"`
	SizeBytes int64    `json:"sizeBytes"`
	Image     string   `json:"image,omitempty"`
	ABI       string   `json:"abi,omitempty"`
	Running   bool     `json:"running"`
	Serial    string   `json:"serial,omitempty"`
	Port      int      `json:"port,omitempty"`
	PID       int      `json:"pid,omitempty"`
	Booted    bool     `json:"booted"`
	UpSince   int64    `json:"upSince,omitempty"`
	Metrics   *Metrics `json:"metrics,omitempty"`
	MacroID   string   `json:"macroId,omitempty"`
}

// MacroStep is a single recorded action. Coordinates are normalized (0..1) so
// a macro replays correctly on any screen size or AVD.
type MacroStep struct {
	Type     string  `json:"type"`               // tap | swipe | text | key | wait
	X        float64 `json:"x,omitempty"`        // normalized 0..1
	Y        float64 `json:"y,omitempty"`        // normalized 0..1
	X2       float64 `json:"x2,omitempty"`       // normalized 0..1 (swipe end)
	Y2       float64 `json:"y2,omitempty"`       // normalized 0..1 (swipe end)
	Duration int     `json:"duration,omitempty"` // swipe duration ms
	Text     string  `json:"text,omitempty"`
	Key      int     `json:"key,omitempty"`
	DelayMs  int     `json:"delayMs,omitempty"` // wait before next step
}

// Macro is an ordered list of recorded steps.
type Macro struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	AppID     string      `json:"appId,omitempty"`
	Steps     []MacroStep `json:"steps"`
	CreatedAt int64       `json:"createdAt"`
}

// MacroRunRequest starts a macro on a device.
type MacroRunRequest struct {
	ID   string `json:"id"`
	Loop bool   `json:"loop"`
}

// APK is an uploaded test APK in the farm's library.
type APK struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	UploadedAt int64  `json:"uploadedAt"`
}

// Golden is a baked image (directory with raw IMG files + metadata).
type Golden struct {
	Name      string `json:"name"`
	BaseName  string `json:"baseName"`
	Image     string `json:"image,omitempty"`
	Device    string `json:"device,omitempty"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	CreatedAt int64  `json:"createdAt"`
}

// BakeJob tracks an in-progress or finished bake.
type BakeJob struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	State      string `json:"state"` // running | done | error
	Message    string `json:"message"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
	Golden     string `json:"golden,omitempty"`
}

// BakeRequest starts a golden-image build with optional APKs baked in.
type BakeRequest struct {
	Name     string   `json:"name"`
	BaseName string   `json:"baseName"`
	Image    string   `json:"image"`
	Device   string   `json:"device"`
	APKIDs   []string `json:"apkIds"`
}

// LaunchRequest creates and boots a clone from a golden image.
type LaunchRequest struct {
	CloneName string `json:"cloneName"`
	Writable  bool   `json:"writable"`
}

// MetricPoint is a single consumption sample.
type MetricPoint struct {
	T   int64   `json:"t"`
	CPU float64 `json:"cpu"`
	Mem int64   `json:"mem"`
}

// Metrics is the latest consumption snapshot plus a short history.
type Metrics struct {
	Name      string        `json:"name"`
	PID       int           `json:"pid"`
	CPU       float64       `json:"cpu"`
	MemBytes  int64         `json:"memBytes"`
	MemPct    float64       `json:"memPct"`
	UptimeSec int64         `json:"uptimeSec"`
	RxBytes   int64         `json:"rxBytes"`
	TxBytes   int64         `json:"txBytes"`
	RxRate    int64         `json:"rxRate"` // bytes/s
	TxRate    int64         `json:"txRate"` // bytes/s
	History   []MetricPoint `json:"history"`
	UpdatedAt int64         `json:"updatedAt"`
}

// SystemInfo describes the host and toolchain.
type SystemInfo struct {
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	SDKRoot    string   `json:"sdkRoot"`
	HostABI    string   `json:"hostAbi"`
	GPU        string   `json:"gpu"`
	Windowed   bool     `json:"windowed"`
	TotalMem   int64    `json:"totalMem"`
	CPUCount   int      `json:"cpuCount"`
	Emulator   string   `json:"emulatorVersion"`
	Images     []string `json:"images"`
	ServerTime int64    `json:"serverTime"`
}

// Settings are runtime-adjustable knobs that affect how emulators launch.
type Settings struct {
	GPU      string `json:"gpu"`
	Windowed bool   `json:"windowed"`
}

// InputRequest is a remote control action forwarded to the device.
type InputRequest struct {
	Type     string  `json:"type"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	X2       float64 `json:"x2"`
	Y2       float64 `json:"y2"`
	Duration int     `json:"duration"`
	Text     string  `json:"text"`
	Key      int     `json:"key"`
}

// ShellRequest runs a command inside the device.
type ShellRequest struct {
	Command string `json:"command"`
}

func nowMillis() int64 { return time.Now().UnixMilli() }
