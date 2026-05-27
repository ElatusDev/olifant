"""Phase B1b — domain embedder training on Modal.

Trains a sentence-transformers model on the (anchor, positive, negative)
triples produced by Phase B1a (olifant dataset embedder-triples), so the
resulting 768-d embedder can replace nomic-embed-text in olifant's RAG
retrieval path for Phase B2/B3 evaluation.

Architecture choices (per olifant-rag-phase-b-prompt.md §1 HARD RULE #4):
  - base model: nomic-ai/nomic-embed-text-v1.5 (768-d to match olifant's
    Ollama-served nomic-embed-text — see Phase B prompt §6 row 0.3)
  - loss: TripletLoss (anchor / positive / hard-negative) — the corpus-
    mined hard negatives are the primary signal; in-batch negatives via
    MultipleNegativesRankingLoss would dilute that
  - single config: batch=64, epochs=3, lr=2e-5, warmup=0.1 — no grid

Entry points (invoked via `modal run internal/embedder/modal_app.py::<name>`):
  train_full   — real run (~30-60 min on A10G, ~$1-3)
  dry_run      — smoke on 100 examples (~3 min, ~$0.10)
  ls           — list /data/embedders/v1/ contents (no GPU; debug)
"""

import modal

APP_NAME = "olifant-embedder-v1"
VOLUME_NAME = "olifant-train-v1"

BASE_MODEL = "nomic-ai/nomic-embed-text-v1.5"
TRIPLES_PATH = "/data/embedder-v1/triples.jsonl"
MODEL_OUT_DIR = "/data/embedders/v1/model"
MANIFEST_PATH = "/data/embedders/v1/manifest.yaml"
LOSS_LOG_PATH = "/data/embedders/v1/loss-log.csv"

BATCH_SIZE = 64
EPOCHS = 3
LEARNING_RATE = 2e-5
WARMUP_RATIO = 0.1
SEED = 42

GPU = "A10G"
TIMEOUT_SEC = 2 * 60 * 60  # 2 h hard ceiling

app = modal.App(APP_NAME)
volume = modal.Volume.from_name(VOLUME_NAME, create_if_missing=False)

train_image = (
    modal.Image.debian_slim(python_version="3.11")
    .pip_install(
        "sentence-transformers>=3.0.0",
        "torch>=2.1.0,<2.6.0",
        "transformers>=4.40.0",
        "einops>=0.7.0",          # nomic-embed-text-v1.5 dependency
        "huggingface_hub>=0.26.0",
        "PyYAML>=6.0",
    )
)


def _load_triples():
    """Load + shuffle triples; hold out 10% (min 50) for triplet eval."""
    import json
    import random

    with open(TRIPLES_PATH) as f:
        triples = [json.loads(ln) for ln in f if ln.strip()]
    if not triples:
        raise RuntimeError(f"no triples in {TRIPLES_PATH}")

    rng = random.Random(SEED)
    rng.shuffle(triples)

    n_eval = max(50, len(triples) // 10)
    eval_set = triples[:n_eval]
    train_set = triples[n_eval:]
    return train_set, eval_set


def _build_examples(triples):
    from sentence_transformers import InputExample
    return [
        InputExample(texts=[t["anchor"], t["positive"], t["negative"]])
        for t in triples
    ]


def _train(train_set, eval_set, *, dry_run: bool):
    import os
    import time

    import yaml
    from sentence_transformers import SentenceTransformer, losses
    from sentence_transformers.evaluation import TripletEvaluator
    from torch.utils.data import DataLoader

    os.makedirs(MODEL_OUT_DIR, exist_ok=True)

    model = SentenceTransformer(BASE_MODEL, trust_remote_code=True)

    train_examples = _build_examples(train_set)
    train_loader = DataLoader(train_examples, shuffle=True, batch_size=BATCH_SIZE)
    train_loss = losses.TripletLoss(model=model)

    eval_anchors = [t["anchor"] for t in eval_set]
    eval_pos = [t["positive"] for t in eval_set]
    eval_neg = [t["negative"] for t in eval_set]
    evaluator = TripletEvaluator(eval_anchors, eval_pos, eval_neg, name="held-out")

    epochs = 1 if dry_run else EPOCHS
    warmup_steps = int(len(train_loader) * epochs * WARMUP_RATIO)

    print(f"train={len(train_set)}  eval={len(eval_set)}  "
          f"batches/epoch={len(train_loader)}  epochs={epochs}  "
          f"warmup_steps={warmup_steps}  lr={LEARNING_RATE}")

    start = time.time()
    model.fit(
        train_objectives=[(train_loader, train_loss)],
        evaluator=evaluator,
        epochs=epochs,
        warmup_steps=warmup_steps,
        evaluation_steps=len(train_loader),  # at the end of each epoch
        output_path=MODEL_OUT_DIR,
        optimizer_params={"lr": LEARNING_RATE},
        save_best_model=True,
        show_progress_bar=True,
        callback=_loss_callback,
    )
    elapsed = time.time() - start

    manifest = {
        "base_model": BASE_MODEL,
        "triples_total": len(train_set) + len(eval_set),
        "train_count": len(train_set),
        "eval_count": len(eval_set),
        "epochs": epochs,
        "batch_size": BATCH_SIZE,
        "learning_rate": LEARNING_RATE,
        "warmup_ratio": WARMUP_RATIO,
        "warmup_steps": warmup_steps,
        "seed": SEED,
        "elapsed_sec": int(elapsed),
        "out_dir": MODEL_OUT_DIR,
        "loss_log": LOSS_LOG_PATH,
        "dry_run": dry_run,
    }
    with open(MANIFEST_PATH, "w") as f:
        yaml.safe_dump(manifest, f, sort_keys=False)
    print(f"manifest: {MANIFEST_PATH}")
    print(f"model:    {MODEL_OUT_DIR}")
    print(f"elapsed:  {elapsed/60:.1f} min")
    return manifest


def _loss_callback(score, epoch, steps):
    """sentence-transformers calls this after each evaluator pass."""
    import os
    header = "epoch,steps,triplet_accuracy\n"
    line = f"{epoch},{steps},{score}\n"
    if not os.path.exists(LOSS_LOG_PATH):
        with open(LOSS_LOG_PATH, "w") as f:
            f.write(header)
    with open(LOSS_LOG_PATH, "a") as f:
        f.write(line)


@app.function(gpu=GPU, image=train_image, volumes={"/data": volume}, timeout=TIMEOUT_SEC)
def train_full():
    """Real B1b training run on the full triples set (~30-60 min on A10G)."""
    train_set, eval_set = _load_triples()
    print(f"loaded {len(train_set) + len(eval_set)} total triples")
    manifest = _train(train_set, eval_set, dry_run=False)
    volume.commit()
    return manifest


@app.function(gpu=GPU, image=train_image, volumes={"/data": volume}, timeout=10 * 60)
def dry_run():
    """Smoke test on 100 examples (~3 min, ~$0.10)."""
    train_set, eval_set = _load_triples()
    train_set = train_set[:100]
    eval_set = eval_set[:50]
    print(f"DRY RUN: train={len(train_set)} eval={len(eval_set)}")
    manifest = _train(train_set, eval_set, dry_run=True)
    volume.commit()
    return manifest


@app.function(image=train_image, volumes={"/data": volume})
def ls():
    """List /data/embedders/* contents — for post-training debug."""
    import os

    for root, _dirs, files in os.walk("/data/embedders"):
        for fn in files:
            p = os.path.join(root, fn)
            try:
                sz = os.path.getsize(p)
                print(f"{p} ({sz:,} B)")
            except OSError as e:
                print(f"{p} (stat error: {e})")
