package channel

// UploadSem limits how many video files may upload at the same time.
// Each file still fans out to the configured hosts, so keeping this low avoids
// overwhelming small RDP/GitHub runners and remote upload APIs.
var UploadSem = make(chan struct{}, 10)
