import { type TreeNode, docIdFromNode } from "./api";

interface Props {
  node: TreeNode;
  expanded: Set<string>;
  selected: string | null;
  onToggle: (nodeId: string) => void;
  onSelect: (docId: string, nodeId: string) => void;
}

export function TreeNodeView({ node, expanded, selected, onToggle, onSelect }: Props) {
  const hasChildren = !!node.children && node.children.length > 0;
  const open = expanded.has(node.id);
  const docId = docIdFromNode(node.id);
  const isLeaf = !hasChildren;
  const isSelected = selected === node.id;

  const onClick = () => {
    if (hasChildren) onToggle(node.id);
    else if (docId) onSelect(docId, node.id);
  };

  return (
    <div className="node">
      <button
        className={`node-row ${isLeaf ? "leaf" : "internal"} ${isSelected ? "selected" : ""}`}
        onClick={onClick}
        title={node.title}
        data-node={node.id}
      >
        {hasChildren ? (
          <span className="caret">{open ? "▾" : "▸"}</span>
        ) : (
          <span className="bullet">·</span>
        )}
        <span className="label">{node.title || node.id}</span>
      </button>
      {hasChildren && open && (
        <div className="children">
          {node.children!.map((c) => (
            <TreeNodeView
              key={c.id}
              node={c}
              expanded={expanded}
              selected={selected}
              onToggle={onToggle}
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
    </div>
  );
}
