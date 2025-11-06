#!/usr/bin/env python3
"""
Python example showing how datasets connect to evals via the origin field.
This example uses the Braintrust API directly (like the Go example does).
"""

import os
import requests


def main():
    api_key = os.environ.get("BRAINTRUST_API_KEY")
    if not api_key:
        print("Error: BRAINTRUST_API_KEY not set")
        return

    base_url = "https://api.braintrust.dev"
    headers = {"Authorization": f"Bearer {api_key}"}

    # Step 1: Get or create project
    print("1️⃣  Getting project...")
    resp = requests.post(
        f"{base_url}/v1/project",
        headers=headers,
        json={"name": "python-origin-demo", "org_name": "Braintrust"},
    )
    project = resp.json()
    project_id = project["id"]
    print(f"   ✓ Project ID: {project_id}")

    # Step 2: Create dataset
    print("\n2️⃣  Creating dataset...")
    resp = requests.post(
        f"{base_url}/v1/dataset",
        headers=headers,
        json={
            "project_id": project_id,
            "name": f"Python Dataset Example",
        },
    )
    dataset = resp.json()
    dataset_id = dataset["id"]
    print(f"   ✓ Dataset ID: {dataset_id}")

    # Step 3: Insert records into dataset
    print("\n3️⃣  Inserting records into dataset...")
    resp = requests.post(
        f"{base_url}/v1/dataset/{dataset_id}/insert",
        headers=headers,
        json={
            "events": [
                {
                    "input": {"text": "hello world"},
                    "expected": {"response": "Hello World"},
                },
                {
                    "input": {"text": "braintrust is awesome"},
                    "expected": {"response": "Braintrust Is Awesome"},
                },
            ]
        },
    )
    print(f"   ✓ Inserted records")

    # Step 4: Fetch records back to see their IDs
    print("\n4️⃣  Fetching records to see their IDs...")
    resp = requests.get(
        f"{base_url}/v1/dataset/{dataset_id}/fetch",
        headers=headers,
        params={"limit": 10},
    )
    fetch_result = resp.json()

    print(f"   ✓ Fetched {len(fetch_result['events'])} records")
    for i, event in enumerate(fetch_result["events"]):
        print(f"\n   Record {i+1}:")
        print(f"     - id: {event.get('id')}")
        print(f"     - _xact_id: {event.get('_xact_id')}")
        print(f"     - created: {event.get('created')}")
        print(f"     - input: {event.get('input')}")
        print(f"     - expected: {event.get('expected')}")

    print("\n" + "="*80)
    print("🎯 HOW ORIGIN WORKS IN PYTHON SDK")
    print("="*80)
    print("""
When Python's Eval() runs with a dataset:

1. It iterates over dataset records which have:
   - id: the record identifier
   - _xact_id: transaction ID
   - created: timestamp
   - input: your input data
   - expected: expected output

2. For each record, it creates an EvalCase with these fields populated:
   datum = EvalCase(
       input=record['input'],
       expected=record['expected'],
       id=record['id'],
       _xact_id=record['_xact_id'],
       created=record['created']
   )

3. When logging the eval span, it checks if datum has id and _xact_id:
   origin = {
       "object_type": "dataset",
       "object_id": dataset.id,
       "id": datum.id,
       "created": datum.created,
       "_xact_id": datum._xact_id,
   } if datum.id and datum._xact_id else None

4. The origin is passed to the span logger:
   experiment.start_span(
       name="eval",
       input=datum.input,
       expected=datum.expected,
       origin=origin  # <-- This links it back to the dataset row
   )

🎉 THE GO SDK DOES THE SAME THING!

In Go (examples/datasets/main.go):
1. Creates dataset via API ✅
2. Inserts records ✅
3. Fetches records with evaluator.Datasets().Get() ✅
4. Each Case has ID, XactID, Created fields populated ✅
5. runCase() sets origin span attribute when fields present ✅

Both implementations now match!
""")

    print(f"\n💡 To see origin in action:")
    print(f"   1. Run the Go example: go run examples/datasets/main.go")
    print(f"   2. Check the Braintrust UI - each eval result will have origin info")
    print(f"   3. The origin links back to the dataset record that generated it")


if __name__ == "__main__":
    main()
