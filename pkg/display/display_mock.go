// +build test

package display

type Display struct{}

func New() *Display { return &Display{} }

func (d *Display) Launch(url string, width, height, depth int) error {
	return nil
}

func (d *Display) Close() {}
