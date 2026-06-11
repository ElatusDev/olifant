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
  inspect      — server-side model validation (manifest, NaN, triplet acc)
  recall_embed — B1c: embed corpus sentences + recall queries with the
                 trained model, print per-query top-K hits as marked JSON
"""

import modal

APP_NAME = "olifant-embedder-v1"
VOLUME_NAME = "olifant-train-v1"

BASE_MODEL = "nomic-ai/nomic-embed-text-v1.5"
TRIPLES_PATH = "/data/embedder-v1/triples.jsonl"
RECALL_SENTENCES_PATH = "/data/embedder-v1/recall/sentences.jsonl"
RECALL_QUERIES_PATH = "/data/embedder-v1/recall/queries.jsonl"
MODEL_OUT_DIR = "/data/embedders/v1/model"
MANIFEST_PATH = "/data/embedders/v1/manifest.yaml"
LOSS_LOG_PATH = "/data/embedders/v1/loss-log.csv"

BATCH_SIZE = 32          # 64 OOM'd nomic-bert-2048 on A10G (22 GiB); halved per B1b retry policy
MAX_SEQ_LEN = 512        # cap nomic's multi-K context: corpus sentences max ~500 tok (p90 ~83); no truncation, bounds attention memory
EPOCHS = 3
LEARNING_RATE = 2e-5
WARMUP_RATIO = 0.1
SEED = 42

GPU = "A100"  # A10G (22 GiB) OOM'd at ~8% even with batch=32/seq=512; A100 (40 GiB) preserves the recipe
TIMEOUT_SEC = 2 * 60 * 60  # 2 h hard ceiling

app = modal.App(APP_NAME)
volume = modal.Volume.from_name(VOLUME_NAME, create_if_missing=False)

train_image = (
    modal.Image.debian_slim(python_version="3.11")
    .pip_install(
        # Pin to the dependency era nomic-bert-2048's trust_remote_code + the
        # scaffold's classic model.fit() were written for. transformers 5.x
        # double-nests the encoder state_dict (encoder.encoder.*) → saved model
        # reloads with random weights → NaN embeddings; it also drove a nan
        # grad_norm. ST 2.x uses the classic fit() loop (no datasets/accelerate).
        "sentence-transformers>=2.7.0,<3.0.0",
        "torch>=2.1.0,<2.6.0",
        "transformers>=4.40.0,<5.0.0",
        "einops>=0.7.0",          # nomic-embed-text-v1.5 dependency
        "huggingface_hub>=0.23.0,<0.26.0",
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

    # Reduce CUDA fragmentation (suggested by the OOM that batch=64 hit).
    os.environ.setdefault("PYTORCH_CUDA_ALLOC_CONF", "expandable_segments:True")

    import time

    import yaml
    from sentence_transformers import SentenceTransformer, losses
    from sentence_transformers.evaluation import TripletEvaluator
    from torch.utils.data import DataLoader

    os.makedirs(MODEL_OUT_DIR, exist_ok=True)

    model = SentenceTransformer(BASE_MODEL, trust_remote_code=True)
    model.max_seq_length = MAX_SEQ_LEN  # nomic defaults to a multi-K context → OOM at batch size; cap to sentence scale

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
        "max_seq_len": MAX_SEQ_LEN,
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


@app.function(gpu=GPU, image=train_image, volumes={"/data": volume}, timeout=10 * 60)
def inspect():
    """Validate the trained B1b model server-side (the local `volume get` path is
    blocked by an expired corp TLS-interception cert). Dumps manifest + loss-log,
    then measures real held-out triplet accuracy + NaN — the HF Trainer log's
    int-truncated '0' accuracy display is unreliable."""
    import os

    import numpy as np
    from sentence_transformers import SentenceTransformer

    for p in (MANIFEST_PATH, LOSS_LOG_PATH):
        print(f"===== {p} =====")
        print(open(p).read() if os.path.exists(p) else "(missing)")

    _train_set, eval_set = _load_triples()  # same SEED → same held-out split as training
    model = SentenceTransformer(MODEL_OUT_DIR, trust_remote_code=True)
    model.max_seq_length = MAX_SEQ_LEN

    enc = lambda xs: model.encode(xs, normalize_embeddings=True, show_progress_bar=False)
    ea = enc([t["anchor"] for t in eval_set])
    ep = enc([t["positive"] for t in eval_set])
    en = enc([t["negative"] for t in eval_set])

    nan_rows = int(
        np.isnan(ea).any(axis=1).sum()
        + np.isnan(ep).any(axis=1).sum()
        + np.isnan(en).any(axis=1).sum()
    )
    sim_pos = (ea * ep).sum(axis=1)
    sim_neg = (ea * en).sum(axis=1)
    acc = float((sim_pos > sim_neg).mean())

    print("===== validation =====")
    print(f"eval triples:                {len(eval_set)}")
    print(f"embedding dim:               {ea.shape[1]}")
    print(f"rows with NaN embedding:     {nan_rows}")
    print(f"mean cos(anchor, positive):  {float(sim_pos.mean()):.4f}")
    print(f"mean cos(anchor, negative):  {float(sim_neg.mean()):.4f}")
    print(f"held-out triplet accuracy:   {acc:.4f}  (cos(a,p) > cos(a,n); ~0.5 = broken/random)")


@app.function(gpu=GPU, image=train_image, volumes={"/data": volume}, timeout=15 * 60)
def recall_embed(top_k: int = 10):
    """B1c candidate-side retrieval: embed every corpus sentence and every
    recall query with the trained model, rank sentences per query by cosine,
    and print the top-K hits as marker-delimited JSON for the Go side to
    parse (stdout is the only artefact channel — `modal volume get` is
    blocked locally by the corp TLS-interception cert)."""
    import json

    import numpy as np
    from sentence_transformers import SentenceTransformer

    def load_jsonl(path):
        with open(path) as f:
            return [json.loads(ln) for ln in f if ln.strip()]

    sentences = load_jsonl(RECALL_SENTENCES_PATH)
    queries = load_jsonl(RECALL_QUERIES_PATH)
    if not sentences or not queries:
        raise RuntimeError(
            f"empty inputs: {len(sentences)} sentences, {len(queries)} queries"
        )
    print(f"recall_embed: {len(sentences)} sentences, {len(queries)} queries, top_k={top_k}")

    model = SentenceTransformer(MODEL_OUT_DIR, trust_remote_code=True)
    model.max_seq_length = MAX_SEQ_LEN

    enc = lambda xs: model.encode(
        xs, normalize_embeddings=True, batch_size=128, show_progress_bar=False
    )
    sv = enc([s["text"] for s in sentences])
    qv = enc([q["text"] for q in queries])

    nan_rows = int(np.isnan(sv).any(axis=1).sum() + np.isnan(qv).any(axis=1).sum())
    if nan_rows:
        raise RuntimeError(f"{nan_rows} NaN embedding rows — model artefact broken")

    sims = qv @ sv.T  # normalized → dot product == cosine
    out = []
    for qi, q in enumerate(queries):
        order = np.argsort(-sims[qi])[:top_k]
        hits = [
            {
                "sentence_id": sentences[si]["id"],
                "source": sentences[si]["source"],
                "score": float(sims[qi][si]),
            }
            for si in order
        ]
        out.append({"query_id": q["id"], "hits": hits})

    print("===OLIFANT_RECALL_JSON===")
    print(json.dumps({"queries": out}))
    print("===END_OLIFANT_RECALL_JSON===")
