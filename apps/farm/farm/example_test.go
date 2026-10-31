package farm_test

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

func ExampleVerifier_Verify() {
	v := farm.NewVerifier() // add farm.WithLocator to check where the plot is
	rep := v.Verify(context.Background(), farm.Request{
		Boundary: farm.Boundary{Corners: []farm.Corner{
			{Point: geo.Point{Lat: 17.0712, Lng: 78.2021}},
			{Point: geo.Point{Lat: 17.0712, Lng: 78.2030}},
			{Point: geo.Point{Lat: 17.0720, Lng: 78.2030}},
			{Point: geo.Point{Lat: 17.0720, Lng: 78.2021}},
		}},
		Declared: &farm.Area{Value: 2, Unit: farm.Acres},
	})
	fmt.Println(rep.Status, rep.Measures.AcresText)
	for _, c := range rep.Checks {
		fmt.Println(c.Severity, c.Message)
	}
	// Output:
	// verified 2 acres 4 guntas
	// info matches the declared 2 acres (measured 2.1 acres)
}
