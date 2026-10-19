package reference

import "encoding/json"

func planJSON(plan Plan) ([]byte, error) {
	return json.Marshal(plan)
}
