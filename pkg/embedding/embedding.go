package embedding

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

type Embedding struct {
	ID     string
	Vector []float64
}

type jsonEmbedding struct {
	ID     string `json:"id"`
	Vector string `json:"vector"`
}

func (e Embedding) MarshalJSON() ([]byte, error) {
	buf := make([]byte, len(e.Vector)*8)
	for i, v := range e.Vector {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}

	temp := jsonEmbedding{
		ID:     e.ID,
		Vector: base64.StdEncoding.EncodeToString(buf),
	}

	return json.Marshal(temp)
}

func (e *Embedding) UnmarshalJSON(data []byte) error {
	var temp jsonEmbedding
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	buf, err := base64.StdEncoding.DecodeString(temp.Vector)
	if err != nil {
		return fmt.Errorf("failed to decode vector base64: %v", err)
	}

	vector := make([]float64, len(buf)/8)
	for i := range vector {
		bits := binary.LittleEndian.Uint64(buf[i*8:])
		vector[i] = math.Float64frombits(bits)
	}

	e.ID = temp.ID
	e.Vector = vector
	return nil
}
