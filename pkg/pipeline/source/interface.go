package source

type Source interface {
	StartRecording() chan struct{}
	EndRecording() chan struct{}
	Close()
}

type writer interface {
	trackMuted()
	trackUnmuted()
	stop()
}
