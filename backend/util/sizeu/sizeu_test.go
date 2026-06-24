package sizeu

import "testing"

func TestParseBinaryBytes(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "1Gi", want: 1 << 30},
		{in: "64Mi", want: 64 << 20},
		{in: " 8Ki ", want: 8 << 10},
		{in: "0Gi", wantErr: true},
		{in: "1G", wantErr: true},
		{in: "1024", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseBinaryBytes(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBinaryBytes() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseBinaryBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseBinaryKilobytes(t *testing.T) {
	got, err := ParseBinaryKilobytes("1Gi")
	if err != nil {
		t.Fatalf("ParseBinaryKilobytes() error = %v", err)
	}
	if got != 1<<20 {
		t.Fatalf("ParseBinaryKilobytes() = %d, want %d", got, int64(1<<20))
	}
}
