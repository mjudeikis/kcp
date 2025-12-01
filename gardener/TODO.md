# TODOs

* Gardener schema does not contain required `x-kubernetes-preserve-unknown-fields: true` on object type fields.
  This causes validation errors in kcp/kubernetes as it is more strict. Need to fix in the gardener itself or delta need to be maintained.
* Gardener not always bumps generation, so if its not bumped, the runner will not pick up changes as it uses watches.
* Its not enough to delete, we need to add annotations to trigger delete.
* Label selector on owned secrets so we can filter them via labels and not wild card list
* Few TODOs in the code.
