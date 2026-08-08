package canvas

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// document_v2_validate_test.go pins the headline happy + sad paths
// (minimal doc, wrong version, bad viewMode, bad shot link, etc.).
// These fill in the gaps: missing-key branches, non-number coercion
// paths, nested-object rejections (element assetBindings, track clips
// type) and the positive coverage for the two non-video track kinds.
// Each pins a path the migrator or handler would otherwise take without
// error on malformed input.

func TestValidateV2_RejectsMissingSchemaVersionKey(t *testing.T) {
	// `schemaVersion` absent is distinct from `schemaVersion: 1` — pin
	// the dedicated "is required" message so a regression that merged
	// the two branches (and mis-routed null as v2) is caught here.
	doc := baseV2Doc()
	delete(doc, "schemaVersion")
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("missing schemaVersion should be rejected")
	}
	if !strings.Contains(err.Error(), "schemaVersion is required") {
		t.Errorf("unexpected error for missing schemaVersion: %q", err.Error())
	}
}

func TestValidateV2_RejectsNonNumericSchemaVersion(t *testing.T) {
	// Clients sometimes JSON-serialise the version as a string "2".
	// coerceNumber rejects that, and the validator must surface it as
	// `must be a number` rather than silently accepting a parseable
	// string. If we ever add string-coercion, this test fires first so
	// the migration is an explicit choice.
	doc := baseV2Doc()
	doc["schemaVersion"] = "2"
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("string schemaVersion should be rejected")
	}
	if !strings.Contains(err.Error(), "must be a number") {
		t.Errorf("error should mention must-be-a-number, got %q", err.Error())
	}
}

func TestValidateV2_RejectsElementsMissingEntirely(t *testing.T) {
	// `elements` is the required anchor array — removing it must
	// surface as `elements is required`, not `elements must be an
	// array`, since the two errors point ops at different fixes (stub
	// the key vs. fix the type). requireArray distinguishes them; pin
	// the missing-branch string.
	doc := baseV2Doc()
	delete(doc, "elements")
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("missing elements should be rejected")
	}
	if !strings.Contains(err.Error(), "elements is required") {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateV2_RejectsElementNotAnObject(t *testing.T) {
	// A scalar entry in the elements array (e.g. a stray element-id
	// string that wasn't wrapped in an object) would bypass the later
	// field checks entirely. Pin the outer type-assert branch.
	doc := baseV2Doc()
	doc["elements"] = []interface{}{"raw-string"}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("non-object element should be rejected")
	}
	if !strings.Contains(err.Error(), "elements[0] must be an object") {
		t.Errorf("unexpected error: %q", err.Error())
	}
}

func TestValidateV2_RejectsViewportScaleNotANumber(t *testing.T) {
	// Viewport shape errors are a common UX confusion source — a buggy
	// client sends `scale: "1"` and the document load silently succeeds
	// if we don't type-check. Pin the per-field message so ops can tell
	// x/y/scale apart in the server logs.
	doc := baseV2Doc()
	doc["viewport"] = map[string]interface{}{
		"x":     float64(0),
		"y":     float64(0),
		"scale": "1",
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("non-numeric scale should be rejected")
	}
	if !strings.Contains(err.Error(), "viewport.scale") {
		t.Errorf("error should mention viewport.scale, got %q", err.Error())
	}
}

func TestValidateV2_RejectsElementAssetBindingScope(t *testing.T) {
	// The per-element assetBindings block is a shallow sub-object that
	// reuses the top-level validator. Pin that an element-scoped bad
	// scope surfaces with the correct path prefix — downstream log
	// dashboards filter on `elements[%d].assetBindings` to triage which
	// element broke.
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":   "el-1",
			"type": "image",
			"x":    float64(0),
			"y":    float64(0),
			"assetBindings": map[string]interface{}{
				"scope":        "global", // not in allowedAssetScopes
				"characterIds": []interface{}{float64(1)},
			},
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("element assetBindings with bad scope should be rejected")
	}
	if !strings.Contains(err.Error(), "elements[0].assetBindings") {
		t.Errorf("error should include path prefix, got %q", err.Error())
	}
}

func TestValidateV2_RejectsUnsupportedElementType(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":   "el-1",
			"type": "iframe",
			"x":    float64(0),
			"y":    float64(0),
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("unsupported element type should be rejected")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Errorf("error should mention type, got %q", err.Error())
	}
}

func TestValidateV2_RejectsElementInvalidCoordinates(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":   "el-1",
			"type": "shape",
			"x":    "0",
			"y":    float64(0),
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("string x coordinate should be rejected")
	}
	if !strings.Contains(err.Error(), "elements[0].x") {
		t.Errorf("error should mention x coordinate, got %q", err.Error())
	}
}

func TestValidateV2_RejectsUnsafeElementURL(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":   "el-1",
			"type": "image",
			"x":    float64(0),
			"y":    float64(0),
			"src":  "javascript:alert(1)",
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("unsafe image src should be rejected")
	}
	if !strings.Contains(err.Error(), "src") {
		t.Errorf("error should mention src, got %q", err.Error())
	}
}

func TestValidateV2_AcceptsElementLayerAndMaskMetadata(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":               "el-1",
			"type":             "image",
			"x":                float64(0),
			"y":                float64(0),
			"layerRole":        "foreground",
			"compositeGroupId": "stack-1",
			"parentElementId":  "source-1",
			"mask": map[string]interface{}{
				"url":             "/uploads/canvas/uid/1/project-a/mask.png",
				"featherPx":       float64(4),
				"expandPx":        float64(-2),
				"invert":          false,
				"sourceElementId": "source-1",
			},
		},
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("layer/mask metadata should validate, got %v", err)
	}
}

func TestValidateV2_RejectsInvalidLayerRole(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":        "el-1",
			"type":      "image",
			"x":         float64(0),
			"y":         float64(0),
			"layerRole": "matte-paint",
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("invalid layerRole should be rejected")
	}
	if !strings.Contains(err.Error(), "layerRole") {
		t.Errorf("error should mention layerRole, got %q", err.Error())
	}
}

func TestValidateV2_RejectsInvalidMaskMetadata(t *testing.T) {
	t.Run("mask must be object", func(t *testing.T) {
		doc := baseV2Doc()
		doc["elements"] = []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "image",
				"x":    float64(0),
				"y":    float64(0),
				"mask": "bad",
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "mask must be an object") {
			t.Fatalf("expected mask object error, got %v", err)
		}
	})

	t.Run("mask url must be safe", func(t *testing.T) {
		doc := baseV2Doc()
		doc["elements"] = []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "image",
				"x":    float64(0),
				"y":    float64(0),
				"mask": map[string]interface{}{
					"url": "javascript:alert(1)",
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "mask.url") {
			t.Fatalf("expected mask.url error, got %v", err)
		}
	})

	t.Run("mask numeric fields must be non-negative", func(t *testing.T) {
		doc := baseV2Doc()
		doc["elements"] = []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "image",
				"x":    float64(0),
				"y":    float64(0),
				"mask": map[string]interface{}{
					"url":       "/uploads/canvas/uid/1/project-a/mask.png",
					"featherPx": float64(-1),
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "featherPx") {
			t.Fatalf("expected featherPx error, got %v", err)
		}
	})

	t.Run("mask expandPx must stay within frontend range", func(t *testing.T) {
		doc := baseV2Doc()
		doc["elements"] = []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "image",
				"x":    float64(0),
				"y":    float64(0),
				"mask": map[string]interface{}{
					"url":      "/uploads/canvas/uid/1/project-a/mask.png",
					"expandPx": float64(-13),
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "expandPx") {
			t.Fatalf("expected expandPx error, got %v", err)
		}
	})

	t.Run("mask invert must be boolean", func(t *testing.T) {
		doc := baseV2Doc()
		doc["elements"] = []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "image",
				"x":    float64(0),
				"y":    float64(0),
				"mask": map[string]interface{}{
					"url":    "/uploads/canvas/uid/1/project-a/mask.png",
					"invert": "false",
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "invert") {
			t.Fatalf("expected invert error, got %v", err)
		}
	})
}

func validWorkflowPayload() map[string]interface{} {
	return map[string]interface{}{
		"nodes": []interface{}{
			map[string]interface{}{
				"id":        "node-input",
				"kind":      "input",
				"elementId": "el-1",
				"position": map[string]interface{}{
					"x": float64(0),
					"y": float64(0),
				},
			},
			map[string]interface{}{
				"id":        "node-edit",
				"kind":      "operation",
				"operation": "inpaint",
				"params": map[string]interface{}{
					"prompt":   "repair masked area",
					"strength": float64(0.75),
				},
				"position": map[string]interface{}{
					"x": float64(260),
					"y": float64(0),
				},
			},
		},
		"edges": []interface{}{
			map[string]interface{}{
				"id":         "edge-input-edit",
				"fromNodeId": "node-input",
				"toNodeId":   "node-edit",
				"fromPort":   "out",
				"toPort":     "in",
			},
		},
	}
}

func TestValidateV2_AcceptsWorkflowMetadata(t *testing.T) {
	doc := baseV2Doc()
	doc["viewMode"] = string(ViewModeWorkflow)
	workflow := validWorkflowPayload()
	workflow["graphVersion"] = float64(2)
	workflow["lastEditedAt"] = float64(100)
	workflow["lastEditedBy"] = "agent"
	doc["workflow"] = workflow
	doc["workflowRuns"] = []interface{}{
		map[string]interface{}{
			"id":                "run-1",
			"status":            "failed",
			"startedAt":         float64(100),
			"completedAt":       float64(200),
			"resumeCount":       float64(1),
			"lastResumedNodeId": "node-edit",
			"lastResumedAt":     float64(150),
			"nodeResults": []interface{}{
				map[string]interface{}{
					"nodeId":          "node-edit",
					"status":          "blocked",
					"inputElementIds": []interface{}{"image-1"},
					"outputElementId": "image-2",
					"taskId":          "task-1",
					"errorMessage":    "Source media is not ready.",
				},
			},
			"summary": map[string]interface{}{
				"generatedImages":  float64(0),
				"imageOps":         float64(0),
				"videoGenerations": float64(0),
				"skipped":          float64(0),
				"failed":           float64(0),
			},
		},
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("valid workflow should pass, got %v", err)
	}
}

func TestValidateV2_RejectsInvalidWorkflowRunInputElementIDs(t *testing.T) {
	doc := baseV2Doc()
	doc["workflowRuns"] = []interface{}{
		map[string]interface{}{
			"id":        "run-1",
			"status":    "failed",
			"startedAt": float64(100),
			"nodeResults": []interface{}{
				map[string]interface{}{
					"nodeId":          "node-edit",
					"status":          "failed",
					"inputElementIds": []interface{}{"image-1", float64(42)},
				},
			},
			"summary": map[string]interface{}{
				"generatedImages":  float64(0),
				"imageOps":         float64(0),
				"videoGenerations": float64(0),
				"skipped":          float64(0),
				"failed":           float64(1),
			},
		},
	}

	err := ValidateCanvasDocumentV2(doc)
	if err == nil || !strings.Contains(err.Error(), "inputElementIds[1] must be a string") {
		t.Fatalf("expected inputElementIds string error, got %v", err)
	}
}

func TestValidateV2_RejectsInvalidWorkflowMetadata(t *testing.T) {
	t.Run("workflow must be object", func(t *testing.T) {
		doc := baseV2Doc()
		doc["workflow"] = "bad"
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow must be an object") {
			t.Fatalf("expected workflow object error, got %v", err)
		}
	})

	t.Run("nodes must be array", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		workflow["nodes"] = "bad"
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.nodes must be an array") {
			t.Fatalf("expected workflow nodes error, got %v", err)
		}
	})

	t.Run("node kind must be supported", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		nodes := workflow["nodes"].([]interface{})
		nodes[0].(map[string]interface{})["kind"] = "comfy"
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.nodes[0].kind") {
			t.Fatalf("expected workflow kind error, got %v", err)
		}
	})

	t.Run("operation must be supported", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		nodes := workflow["nodes"].([]interface{})
		nodes[1].(map[string]interface{})["operation"] = "train-model"
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.nodes[1].operation") {
			t.Fatalf("expected workflow operation error, got %v", err)
		}
	})

	t.Run("position must be finite", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		nodes := workflow["nodes"].([]interface{})
		position := nodes[0].(map[string]interface{})["position"].(map[string]interface{})
		position["x"] = math.Inf(1)
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.nodes[0].position.x") {
			t.Fatalf("expected workflow position error, got %v", err)
		}
	})

	t.Run("params must be bounded", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		nodes := workflow["nodes"].([]interface{})
		params := map[string]interface{}{}
		for i := 0; i < maxCanvasWorkflowParamsKeys+1; i++ {
			params[fmt.Sprintf("k%d", i)] = "v"
		}
		nodes[1].(map[string]interface{})["params"] = params
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "params exceeds") {
			t.Fatalf("expected workflow params error, got %v", err)
		}
	})

	t.Run("edge ids must be valid", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		edges := workflow["edges"].([]interface{})
		edges[0].(map[string]interface{})["toNodeId"] = ""
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.edges[0].toNodeId") {
			t.Fatalf("expected workflow edge id error, got %v", err)
		}
	})

	t.Run("edge endpoints must reference existing nodes", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		edges := workflow["edges"].([]interface{})
		edges[0].(map[string]interface{})["toNodeId"] = "missing-node"
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "references missing workflow node") {
			t.Fatalf("expected workflow missing node error, got %v", err)
		}
	})

	t.Run("node ids must be unique", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		nodes := workflow["nodes"].([]interface{})
		nodes[1].(map[string]interface{})["id"] = "node-input"
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("expected workflow duplicate node error, got %v", err)
		}
	})

	t.Run("edge ids must be unique", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		edges := workflow["edges"].([]interface{})
		edges = append(edges, map[string]interface{}{
			"id":         "edge-input-edit",
			"fromNodeId": "node-input",
			"toNodeId":   "node-edit",
		})
		workflow["edges"] = edges
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("expected workflow duplicate edge error, got %v", err)
		}
	})
}

func TestValidateV2_RejectsInvalidWorkflowRuns(t *testing.T) {
	t.Run("workflowRuns must be array", func(t *testing.T) {
		doc := baseV2Doc()
		doc["workflowRuns"] = "bad"
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflowRuns must be an array") {
			t.Fatalf("expected workflowRuns array error, got %v", err)
		}
	})

	t.Run("workflowRuns are capped", func(t *testing.T) {
		doc := baseV2Doc()
		runs := make([]interface{}, 0, maxCanvasWorkflowRuns+1)
		for i := 0; i < maxCanvasWorkflowRuns+1; i++ {
			runs = append(runs, map[string]interface{}{
				"id":          fmt.Sprintf("run-%d", i),
				"status":      "completed",
				"startedAt":   float64(i),
				"nodeResults": []interface{}{},
				"summary": map[string]interface{}{
					"generatedImages":  float64(0),
					"imageOps":         float64(0),
					"videoGenerations": float64(0),
					"skipped":          float64(0),
					"failed":           float64(0),
				},
			})
		}
		doc["workflowRuns"] = runs
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflowRuns exceeds") {
			t.Fatalf("expected workflowRuns cap error, got %v", err)
		}
	})

	t.Run("run status must be supported", func(t *testing.T) {
		doc := baseV2Doc()
		doc["workflowRuns"] = []interface{}{
			map[string]interface{}{
				"id":          "run-bad",
				"status":      "bad",
				"startedAt":   float64(100),
				"nodeResults": []interface{}{},
				"summary": map[string]interface{}{
					"generatedImages":  float64(0),
					"imageOps":         float64(0),
					"videoGenerations": float64(0),
					"skipped":          float64(0),
					"failed":           float64(0),
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflowRuns[0].status") {
			t.Fatalf("expected workflowRuns status error, got %v", err)
		}
	})

	t.Run("running workflowRuns are volatile only", func(t *testing.T) {
		doc := baseV2Doc()
		doc["workflowRuns"] = []interface{}{
			map[string]interface{}{
				"id":          "run-live",
				"status":      "running",
				"startedAt":   float64(100),
				"nodeResults": []interface{}{},
				"summary": map[string]interface{}{
					"generatedImages":  float64(0),
					"imageOps":         float64(0),
					"videoGenerations": float64(0),
					"skipped":          float64(0),
					"failed":           float64(0),
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "not persistable") {
			t.Fatalf("expected workflowRuns running status error, got %v", err)
		}
	})

	t.Run("run resume count must be a non-negative integer", func(t *testing.T) {
		doc := baseV2Doc()
		doc["workflowRuns"] = []interface{}{
			map[string]interface{}{
				"id":          "run-bad",
				"status":      "failed",
				"startedAt":   float64(100),
				"resumeCount": float64(-1),
				"nodeResults": []interface{}{},
				"summary": map[string]interface{}{
					"generatedImages":  float64(0),
					"imageOps":         float64(0),
					"videoGenerations": float64(0),
					"skipped":          float64(0),
					"failed":           float64(1),
				},
			},
		}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "resumeCount") {
			t.Fatalf("expected workflowRuns resumeCount error, got %v", err)
		}
	})
}

func TestValidateV2_RejectsInvalidWorkflowVersionMetadata(t *testing.T) {
	t.Run("graph version must be positive integer", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		workflow["graphVersion"] = float64(0)
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.graphVersion") {
			t.Fatalf("expected graphVersion error, got %v", err)
		}
	})

	t.Run("last edited by must be supported", func(t *testing.T) {
		doc := baseV2Doc()
		workflow := validWorkflowPayload()
		workflow["lastEditedBy"] = "bot"
		doc["workflow"] = workflow
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "workflow.lastEditedBy") {
			t.Fatalf("expected lastEditedBy error, got %v", err)
		}
	})
}

func TestValidateV2_RejectsAssetBindingNonNumericID(t *testing.T) {
	// Asset ids must be numeric — a string id would cascade into the
	// injector (which looks them up in the character/brand/product
	// tables by int id) and silently return zero bindings. Pin the
	// coerceNumber reject path.
	doc := baseV2Doc()
	doc["projectAssetBindings"] = map[string]interface{}{
		"scope":        string(AssetScopeProject),
		"characterIds": []interface{}{float64(1), "two"},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("non-numeric character id should be rejected")
	}
	if !strings.Contains(err.Error(), "characterIds[1]") {
		t.Errorf("error should point at the bad index, got %q", err.Error())
	}
}

func TestValidateV2_AcceptsAllTimelineTrackKinds(t *testing.T) {
	// The validator allows three track kinds — video/audio/subtitle.
	// The existing suite only exercises video. Pin each so a regression
	// that dropped one from allowedTrackKinds is caught here.
	for _, kind := range []string{
		string(TimelineTrackVideo),
		string(TimelineTrackAudio),
		string(TimelineTrackSubtitle),
	} {
		t.Run(kind, func(t *testing.T) {
			doc := baseV2Doc()
			doc["timeline"] = map[string]interface{}{
				"fps":           float64(30),
				"totalDuration": float64(60000),
				"tracks": []interface{}{
					map[string]interface{}{
						"id":    "track-" + kind,
						"kind":  kind,
						"clips": []interface{}{},
					},
				},
			}
			if err := ValidateCanvasDocumentV2(doc); err != nil {
				t.Errorf("track kind %q should be accepted, got %v", kind, err)
			}
		})
	}
}

func TestValidateV2_RejectsShotTypeNameMismatch(t *testing.T) {
	// typeName is optional on a shot, but when present must equal
	// WorkMaxTypeShot. A wrong value would let a non-shot object slip into
	// the `shots[]` array and break shot-sync, which keys by typeName.
	doc := baseV2Doc()
	doc["shots"] = []interface{}{
		map[string]interface{}{
			"id":          "shot-1",
			"typeName":    "not-a-shot",
			"shotId":      float64(1),
			"localCardId": "card-1",
			"orderIndex":  float64(0),
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("shot with wrong typeName should be rejected")
	}
	if !strings.Contains(err.Error(), "typeName") {
		t.Errorf("error should mention typeName, got %q", err.Error())
	}
}
