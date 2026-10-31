package geo_test

import (
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

func ExampleArea() {
	// A plot near Shadnagar, Telangana, corners as a phone gives them.
	plot := geo.Ring{
		{Lat: 17.0712, Lng: 78.2021},
		{Lat: 17.0712, Lng: 78.2030},
		{Lat: 17.0720, Lng: 78.2030},
		{Lat: 17.0720, Lng: 78.2021},
	}
	fmt.Printf("%.0f m², %.0f m around, crossings: %d\n", geo.Area(plot), geo.Perimeter(plot), len(geo.SelfIntersections(plot)))
	// Output: 8482 m², 369 m around, crossings: 0
}
