package mobileguide

import (
	"math"
	"sort"
	"strconv"
)

// The adaptive (per-breakpoint) layout and the positional anchor-row placement.

type webCell struct{ column, row, span, rowSpan int }

var stackedColumns = []string{"1fr"}

func buildAdaptiveLayout(elementMap *[]*mapEntry, sourceLayouts *dict[*jn], gridContainerColumns *dict[int],
	mobileContainerParents *dict[string]) []*AdaptiveLayoutGroup {
	colsByMobileParent := newCI[int]()
	for _, entry := range *elementMap {
		if entry.WebName != nil && *entry.WebName != "" {
			if cols, ok := gridContainerColumns.get(*entry.WebName); ok && eqi(deref(entry.MobileType), mobileGridContainerType) {
				key := *entry.WebName
				if entry.Name != nil && *entry.Name != "" {
					key = *entry.Name
				}
				colsByMobileParent.set(key, cols)
			}
		}
	}
	for _, pair := range gridContainerColumns.pairs() {
		colsByMobileParent.tryAdd(pair.key, pair.value)
	}
	inserted := newSet(true)
	for _, entry := range *elementMap {
		if isInsert(entry) && entry.Name != nil && *entry.Name != "" {
			inserted.add(*entry.Name)
		}
	}
	byContainer := newCI[[]*mapEntry]()
	var order []string
	for _, entry := range *elementMap {
		placeableTwin := false
		if isMerge(entry) && entry.MergeParentName != nil && *entry.MergeParentName != "" && entry.Name != nil && *entry.Name != "" &&
			!inserted.contains(*entry.Name) && mobileContainerParents != nil {
			if mobileParent, ok := mobileContainerParents.get(*entry.Name); ok && eqi(mobileParent, *entry.MergeParentName) &&
				colsByMobileParent.has(*entry.MergeParentName) {
				placeableTwin = true
			}
		}
		var parent string
		if placeableTwin {
			if entry.Values != nil && !entry.Values.isObj() {
				continue
			}
			parent = *entry.MergeParentName
		} else {
			if !isInsert(entry) || !entry.Values.isObj() {
				continue
			}
			parent = deref(entry.ParentName)
		}
		if parent == "" || !colsByMobileParent.has(parent) {
			continue
		}
		if entry.PositionalAnchor != nil && *entry.PositionalAnchor != "" {
			continue
		}
		list, ok := byContainer.get(parent)
		if !ok {
			order = append(order, parent)
		}
		byContainer.set(parent, append(list, entry))
	}
	taken := newSet(true)
	for _, entry := range *elementMap {
		if entry.WebName != nil && *entry.WebName != "" {
			taken.add(*entry.WebName)
		}
		if entry.Name != nil && *entry.Name != "" {
			taken.add(*entry.Name)
		}
	}
	var groups []*AdaptiveLayoutGroup
	for _, container := range order {
		webCols := colsByMobileParent.value(container)
		if webCols <= 1 {
			continue
		}
		children, _ := byContainer.get(container)
		cells := make([]webCell, len(children))
		for index, child := range children {
			cells[index] = webCellOf(sourceLayouts, child.WebName, index, webCols)
		}
		var containerEntry *mapEntry
		for _, entry := range *elementMap {
			if (isInsert(entry) || isMerge(entry)) && eqi(deref(entry.Name), container) && entry.Name != nil {
				containerEntry = entry
				break
			}
		}
		bands := bandsOf(containerEntry, children, cells)
		if len(bands) > 1 {
			groups = append(groups, placeInBands(elementMap, containerEntry, container, children, cells, bands, taken)...)
			continue
		}
		var weights []string
		tabletCells := make([][2]int, len(children))
		if len(bands) == 1 {
			layout := layOutBand(cells, bands[0])
			weights = layout.weights
			for index, cell := range layout.cells {
				tabletCells[index] = cell
			}
		} else {
			tablet := buildTabletGrid(children, cells)
			weights = tablet.weights
			for index := range children {
				tabletCells[index] = [2]int{tablet.columns[index], tablet.rows[index]}
			}
		}
		var items []*AdaptiveLayoutItem
		for index, child := range children {
			items = append(items, placeAdaptive(child, adaptiveCell(1, index+1), adaptiveCell(tabletCells[index][0], tabletCells[index][1])))
		}
		writeContainerColumns(containerEntry, weights)
		groups = append(groups, layoutGroup(container, weights, items))
	}
	return groups
}

func adaptiveCell(column, row int) *jn {
	cell := newObj()
	cell.set("row", newIntNode(row))
	cell.set("column", newIntNode(column))
	cell.set("colSpan", newIntNode(1))
	cell.set("rowSpan", newIntNode(1))
	return cell
}

func layoutGroup(container string, weights []string, items []*AdaptiveLayoutItem) *AdaptiveLayoutGroup {
	columns := &OrderedMap[[]string]{}
	columns.Set("small", stackedColumns)
	columns.Set("medium", weights)
	columns.Set("large", weights)
	if items == nil {
		items = []*AdaptiveLayoutItem{}
	}
	return &AdaptiveLayoutGroup{ContainerName: strp(container), ColumnsByBreakpoint: columns, Items: items}
}

func placeAdaptive(child *mapEntry, phoneCell, tabletCell *jn) *AdaptiveLayoutItem {
	adaptive := newObj()
	adaptive.set("small", phoneCell)
	adaptive.set("medium", tabletCell)
	adaptive.set("large", tabletCell.clone())
	if child.Values == nil {
		child.Values = newObj()
	}
	layout := newObj()
	layout.set("adaptive", adaptive.clone())
	child.Values.set("layoutConfig", layout)
	return &AdaptiveLayoutItem{Name: child.Name, LayoutConfigAdaptive: rawOf(adaptive)}
}

func columnsNode(columns []string) *jn {
	array := newArr()
	for _, column := range columns {
		array.arr = append(array.arr, newStr(column))
	}
	return array
}

func writeContainerColumns(containerEntry *mapEntry, tabletColumns []string) {
	if containerEntry == nil {
		return
	}
	if !containerEntry.Values.isObj() {
		containerEntry.Values = newObj()
	}
	values := containerEntry.Values
	adaptive := newObj()
	for _, breakpoint := range []struct {
		name    string
		columns []string
	}{{"small", stackedColumns}, {"medium", tabletColumns}, {"large", tabletColumns}} {
		slot := newObj()
		slot.set("columns", columnsNode(breakpoint.columns))
		adaptive.set(breakpoint.name, slot)
	}
	values.set("adaptive", adaptive)
	if isInsert(containerEntry) && eqi(deref(containerEntry.MobileType), mobileGridContainerType) {
		values.set("columns", columnsNode(tabletColumns))
	}
}

func bandsOf(containerEntry *mapEntry, children []*mapEntry, cells []webCell) [][]int {
	if !isInsert(containerEntry) || !eqi(deref(containerEntry.MobileType), mobileGridContainerType) {
		return nil
	}
	for _, child := range children {
		if !isInsert(child) {
			return nil
		}
	}
	bands := splitIntoBands(cells)
	if bands == nil {
		return nil
	}
	for _, band := range bands {
		distinctColumns := map[int]bool{}
		for _, index := range band {
			distinctColumns[cells[index].column] = true
		}
		if len(distinctColumns) > mobileGridMaxColumns {
			return nil
		}
	}
	return bands
}

// groupByRow is Enumerable.Range(0, n).GroupBy(i => cells[i].Row).OrderBy(g => g.Key).
func groupByRow(cells []webCell) [][]int {
	var keys []int
	groups := map[int][]int{}
	for index, cell := range cells {
		if _, ok := groups[cell.row]; !ok {
			keys = append(keys, cell.row)
		}
		groups[cell.row] = append(groups[cell.row], index)
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := make([][]int, len(keys))
	for index, key := range keys {
		result[index] = groups[key]
	}
	return result
}

func splitIntoBands(cells []webCell) [][]int {
	var bands [][]int
	coveredThroughRow := 0
	for _, rowChildren := range groupByRow(cells) {
		rowKey := cells[rowChildren[0]].row
		if !sharesColumnStructure(cells, rowChildren) {
			return nil
		}
		var current []int
		if len(bands) > 0 {
			current = bands[len(bands)-1]
		}
		joins := current != nil && rowFitsBand(cells, current, rowChildren)
		if current != nil && rowKey <= coveredThroughRow && !joins {
			return nil
		}
		if joins {
			bands[len(bands)-1] = append(current, rowChildren...)
		} else {
			bands = append(bands, append([]int{}, rowChildren...))
		}
		maxRow := math.MinInt
		for _, index := range rowChildren {
			maxRow = max(maxRow, cells[index].row+cells[index].rowSpan-1)
		}
		coveredThroughRow = max(coveredThroughRow, maxRow)
	}
	return bands
}

func sharesColumnStructure(cells []webCell, members []int) bool {
	for _, a := range members {
		for _, b := range members {
			if a != b && covers(cells[a], cells[b]) {
				return false
			}
		}
	}
	return true
}

func rowFitsBand(cells []webCell, band, row []int) bool {
	for _, a := range band {
		for _, b := range row {
			if covers(cells[a], cells[b]) || covers(cells[b], cells[a]) {
				return false
			}
		}
	}
	return true
}

func covers(wide, other webCell) bool {
	return other.column > wide.column && other.column <= wide.column+wide.span-1
}

func orderedByRowThenColumn(cells []webCell, band []int) []int {
	ordered := append([]int{}, band...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := cells[ordered[i]], cells[ordered[j]]
		if a.row != b.row {
			return a.row < b.row
		}
		return a.column < b.column
	})
	return ordered
}

func placeInBands(elementMap *[]*mapEntry, containerEntry *mapEntry, container string, children []*mapEntry, cells []webCell,
	bands [][]int, taken *strSet) []*AdaptiveLayoutGroup {
	var groups []*AdaptiveLayoutGroup
	type unit struct {
		entry     *mapEntry
		treeIndex int
	}
	var units []unit
	bandNumber := 0
	for _, band := range bands {
		ordered := orderedByRowThenColumn(cells, band)
		distinctColumns := map[int]bool{}
		for _, index := range ordered {
			distinctColumns[cells[index].column] = true
		}
		if len(distinctColumns) == 1 {
			for _, index := range ordered {
				units = append(units, unit{children[index], index})
			}
			continue
		}
		layout := layOutBand(cells, band)
		bandNumber++
		bandName := bandNameFor(container, bandNumber, taken)
		bandEntry := bandGridEntry(bandName, container, containerEntry, layout.weights)
		minPosition, minIndex := math.MaxInt, math.MaxInt
		for _, index := range band {
			minPosition = min(minPosition, indexOfEntry(*elementMap, children[index]))
			minIndex = min(minIndex, index)
		}
		*elementMap = insertEntry(*elementMap, minPosition, bandEntry)
		units = append(units, unit{bandEntry, minIndex})
		treeOrder := append([]int{}, band...)
		sort.Ints(treeOrder)
		var bandItems []*AdaptiveLayoutItem
		for _, index := range ordered {
			child := children[index]
			child.ParentName = strp(bandName)
			child.PropertyName = strp(itemsPropertyName)
			child.Index = nil
			cell := layout.cells[index]
			bandItems = append(bandItems, placeAdaptive(child, adaptiveCell(1, positionIn(treeOrder, index)+1), adaptiveCell(cell[0], cell[1])))
		}
		groups = append(groups, layoutGroup(bandName, layout.weights, bandItems))
	}
	phoneOrder := make([]int, len(units))
	for index := range phoneOrder {
		phoneOrder[index] = index
	}
	sort.SliceStable(phoneOrder, func(i, j int) bool { return units[phoneOrder[i]].treeIndex < units[phoneOrder[j]].treeIndex })
	var unitItems []*AdaptiveLayoutItem
	for position, item := range units {
		unitItems = append(unitItems, placeAdaptive(item.entry, adaptiveCell(1, positionIn(phoneOrder, position)+1), adaptiveCell(1, position+1)))
	}
	writeContainerColumns(containerEntry, stackedColumns)
	return append([]*AdaptiveLayoutGroup{layoutGroup(container, stackedColumns, unitItems)}, groups...)
}

func positionIn(values []int, value int) int {
	for index, item := range values {
		if item == value {
			return index
		}
	}
	return -1
}

type bandLayout struct {
	weights []string
	cells   map[int][2]int
}

func layOutBand(cells []webCell, band []int) bandLayout {
	ordered := orderedByRowThenColumn(cells, band)
	var starts []int
	seen := map[int]bool{}
	for _, index := range ordered {
		if !seen[cells[index].column] {
			seen[cells[index].column] = true
			starts = append(starts, cells[index].column)
		}
	}
	sort.Ints(starts)
	spans := make([]int, len(starts))
	for k, start := range starts {
		widest := math.MinInt
		for _, index := range ordered {
			if cells[index].column == start {
				widest = max(widest, cells[index].span)
			}
		}
		limit := math.MaxInt32
		if k+1 < len(starts) {
			limit = starts[k+1] - start
		}
		spans[k] = min(widest, limit)
	}
	firstRow := cells[ordered[0]].row
	bandCells := make([]webCell, len(ordered))
	bandColumns := make([]int, len(ordered))
	for position, index := range ordered {
		cell := cells[index]
		cell.row = cell.row - firstRow + 1
		bandCells[position] = cell
		bandColumns[position] = positionIn(starts, cells[index].column)
	}
	placedColumns, placedRows := placeInRows(bandCells, bandColumns, make([]bool, len(starts)))
	result := bandLayout{weights: reducedWeights(spans), cells: map[int][2]int{}}
	for position, index := range ordered {
		result.cells[index] = [2]int{placedColumns[position], placedRows[position]}
	}
	return result
}

func reducedWeights(weights []int) []string {
	divisor := 0
	for _, weight := range weights {
		divisor = gcd(divisor, weight)
	}
	result := make([]string, len(weights))
	for index, weight := range weights {
		result[index] = strconv.Itoa(weight/divisor) + "fr"
	}
	return result
}

func gcd(a, b int) int {
	if b == 0 {
		return a
	}
	return gcd(b, a%b)
}

func bandNameFor(container string, bandNumber int, taken *strSet) string {
	name := container + "_TabletBand" + strconv.Itoa(bandNumber)
	for suffix := 2; !taken.add(name); suffix++ {
		name = container + "_TabletBand" + strconv.Itoa(bandNumber) + "_" + strconv.Itoa(suffix)
	}
	return name
}

func bandGridEntry(name, container string, containerEntry *mapEntry, weights []string) *mapEntry {
	values := newObj()
	values.set("type", newStr(mobileGridContainerType))
	values.set("columns", columnsNode(weights))
	adaptive := newObj()
	for _, breakpoint := range []struct {
		name    string
		columns []string
	}{{"small", stackedColumns}, {"medium", weights}, {"large", weights}} {
		slot := newObj()
		slot.set("columns", columnsNode(breakpoint.columns))
		adaptive.set(breakpoint.name, slot)
	}
	values.set("adaptive", adaptive)
	if containerEntry != nil && containerEntry.Values.isObj() {
		if gap := containerEntry.Values.get("gap"); gap != nil {
			values.set("gap", gap.clone())
		}
	}
	return &mapEntry{Operation: OperationInsert, Name: strp(name), MobileType: strp(mobileGridContainerType), ParentName: strp(container),
		PropertyName: strp(itemsPropertyName), Values: values}
}

var mobileListComponentTypes = newSet(true, "crt.List", "crt.FileList")

type tabletGrid struct {
	weights       []string
	columns, rows []int
}

func buildTabletGrid(children []*mapEntry, cells []webCell) tabletGrid {
	rowGroups := groupByRow(cells)
	sort.SliceStable(rowGroups, func(i, j int) bool { return len(rowGroups[i]) > len(rowGroups[j]) })
	referenceRow := cells[rowGroups[0][0]].row
	var reference []webCell
	for _, cell := range cells {
		if cell.row == referenceRow {
			reference = append(reference, cell)
		}
	}
	sort.SliceStable(reference, func(i, j int) bool { return reference[i].column < reference[j].column })
	if len(reference) > mobileGridMaxColumns {
		reference = reference[:mobileGridMaxColumns]
	}
	boundaries := make([]int, len(reference))
	for index, cell := range reference {
		boundaries[index] = cell.column
	}
	boundaries[0] = 1
	slots := make([]int, len(cells))
	for index, cell := range cells {
		slots[index] = slotOfBoundaries(boundaries, cell.column)
	}
	joinsNext := make([]bool, len(boundaries))
	for index, cell := range cells {
		if cell.row == referenceRow || !mobileListComponentTypes.contains(deref(children[index].MobileType)) {
			continue
		}
		lastSlot := slotOfBoundaries(boundaries, cell.column+cell.span-1)
		for k := slots[index]; k < lastSlot; k++ {
			joinsNext[k] = true
		}
	}
	columnOfSlot := make([]int, len(boundaries))
	for k := 1; k < len(boundaries); k++ {
		columnOfSlot[k] = columnOfSlot[k-1]
		if !joinsNext[k-1] {
			columnOfSlot[k]++
		}
	}
	columnCount := columnOfSlot[len(columnOfSlot)-1] + 1
	weights := make([]int, columnCount)
	slotsPerColumn := make([]int, columnCount)
	for k := range boundaries {
		weights[columnOfSlot[k]] += reference[k].span
		slotsPerColumn[columnOfSlot[k]]++
	}
	childColumns := make([]int, len(cells))
	for index, slot := range slots {
		childColumns[index] = columnOfSlot[slot]
	}
	merged := make([]bool, columnCount)
	for index, count := range slotsPerColumn {
		merged[index] = count > 1
	}
	columns, rows := placeInRows(cells, childColumns, merged)
	return tabletGrid{weights: reducedWeights(weights), columns: columns, rows: rows}
}

func slotOfBoundaries(boundaries []int, column int) int {
	slot := 0
	for k := 1; k < len(boundaries); k++ {
		if boundaries[k] <= column {
			slot = k
		}
	}
	return slot
}

func placeInRows(cells []webCell, childColumns []int, isMerged []bool) ([]int, []int) {
	columns := make([]int, len(cells))
	rows := make([]int, len(cells))
	rowShift := 0
	for _, webRow := range groupByRow(cells) {
		rowKey := cells[webRow[0]].row
		lines := []map[int]bool{{}}
		ordered := append([]int{}, webRow...)
		sort.SliceStable(ordered, func(i, j int) bool { return cells[ordered[i]].column < cells[ordered[j]].column })
		for _, index := range ordered {
			column := childColumns[index]
			if lines[0][column] && !isMerged[column] {
				candidate := column
				found := false
				for c := column + 1; c < column+1+max(0, len(isMerged)-column-1); c++ {
					if !lines[0][c] && !isMerged[c] {
						candidate, found = c, true
						break
					}
				}
				if !found {
					for c := column - 1; c >= 0; c-- {
						if !lines[0][c] && !isMerged[c] {
							candidate, found = c, true
							break
						}
					}
				}
				column = candidate
			}
			line := 0
			for line < len(lines) && lines[line][column] {
				line++
			}
			if line == len(lines) {
				lines = append(lines, map[int]bool{})
			}
			lines[line][column] = true
			columns[index] = column + 1
			rows[index] = rowKey + rowShift + line
		}
		rowShift += len(lines) - 1
	}
	return columns, rows
}

func readInt(obj *jn, prop string) *int {
	value := obj.get(prop)
	if value == nil || value.k != jsNumber || value.float {
		return nil
	}
	parsed, err := strconv.ParseInt(value.s, 10, 32)
	if err != nil {
		return nil
	}
	result := int(parsed)
	return &result
}

func webCellOf(sourceLayouts *dict[*jn], name *string, index, cols int) webCell {
	var layout *jn
	if name != nil {
		layout, _ = sourceLayouts.get(*name)
	}
	pick := func(prop string, fallback int) int {
		if layout != nil {
			if value := readInt(layout, prop); value != nil {
				return *value
			}
		}
		return fallback
	}
	column := clamp(pick("column", index%cols+1), 1, maxWebPosition)
	row := clamp(pick("row", index/cols+1), 1, maxWebPosition)
	span := pick("colSpan", 1)
	rowSpan := pick("rowSpan", 1)
	return webCell{column: column, row: row, span: clamp(span, 1, max(1, cols-column+1)), rowSpan: clamp(rowSpan, 1, maxWebPosition)}
}

func clamp(value, low, high int) int {
	if low > high {
		panic(engineFault{"'" + strconv.Itoa(low) + "' cannot be greater than " + strconv.Itoa(high) + "."})
	}
	return min(max(value, low), high)
}

// --- positional anchor rows ------------------------------------------------------------------------------

func placePositionalGroups(elementMap *[]*mapEntry, mobileLayoutConfigs *dict[*jn]) {
	if mobileLayoutConfigs.len() == 0 {
		return
	}
	groups := newCI[[]*mapEntry]()
	var order []string
	for _, entry := range *elementMap {
		if isInsert(entry) && entry.PositionalAnchor != nil && *entry.PositionalAnchor != "" {
			list, ok := groups.get(*entry.PositionalAnchor)
			if !ok {
				order = append(order, *entry.PositionalAnchor)
			}
			groups.set(*entry.PositionalAnchor, append(list, entry))
		}
	}
	for _, key := range order {
		group, _ := groups.get(key)
		var above, below []*mapEntry
		for _, entry := range group {
			if entry.Index != nil {
				above = append(above, entry)
			} else {
				below = append(below, entry)
			}
		}
		sort.SliceStable(above, func(i, j int) bool { return *above[i].Index < *above[j].Index })
		anchorPlacement, ok := mobileLayoutConfigs.get(key)
		if (len(above) == 0 && len(below) == 0) || !ok || !carriesRow(anchorPlacement) {
			continue
		}
		placedAbove := 0
		for _, sibling := range above {
			if placeSibling(sibling, anchorPlacement, placedAbove) {
				placedAbove++
			}
		}
		belowOffset := placedAbove + 1
		for _, sibling := range below {
			if placeSibling(sibling, anchorPlacement, belowOffset) {
				belowOffset++
			}
		}
		if placedAbove > 0 {
			setAnchorPlacement(elementMap, key, shiftRows(anchorPlacement, placedAbove))
		}
	}
}

func carriesRow(placement *jn) bool { return len(rowSlots(placement)) > 0 }

func rowSlots(placement *jn) []*jn {
	if placement == nil {
		return nil
	}
	var slots []*jn
	if _, ok := tryRow(placement); ok {
		slots = append(slots, placement)
	}
	for _, slot := range adaptiveRowSlots(placement) {
		slots = append(slots, slot.slot)
	}
	return slots
}

type keyedSlot struct {
	key  string
	slot *jn
}

func adaptiveRowSlots(placement *jn) []keyedSlot {
	adaptive := placement.get("adaptive")
	if !adaptive.isObj() {
		return nil
	}
	var slots []keyedSlot
	for _, key := range adaptive.keys {
		if slot := adaptive.props[key]; slot.isObj() {
			if _, ok := tryRow(slot); ok {
				slots = append(slots, keyedSlot{key, slot})
			}
		}
	}
	return slots
}

func tryRow(slot *jn) (int, bool) {
	if slot == nil {
		return 0, false
	}
	return slot.get("row").tryInt()
}

func shiftRows(placement *jn, by int) *jn {
	shifted := placement.clone()
	for _, slot := range rowSlots(shifted) {
		if row, ok := tryRow(slot); ok {
			slot.set("row", newIntNode(row+by))
		}
	}
	return shifted
}

func placeSibling(sibling *mapEntry, anchorPlacement *jn, offset int) bool {
	values := sibling.Values
	if !values.isObj() {
		return false
	}
	if breakpoints := adaptiveRowSlots(anchorPlacement); len(breakpoints) > 0 {
		adaptive := newObj()
		for _, breakpoint := range breakpoints {
			row, _ := tryRow(breakpoint.slot)
			adaptive.set(breakpoint.key, siblingSlot(row+offset))
		}
		layout := newObj()
		layout.set("adaptive", adaptive)
		values.set("layoutConfig", layout)
		return true
	}
	flatRow, ok := tryRow(anchorPlacement)
	if !ok {
		return false
	}
	values.set("layoutConfig", siblingSlot(flatRow+offset))
	return true
}

func setAnchorPlacement(elementMap *[]*mapEntry, anchor string, placement *jn) {
	for _, entry := range *elementMap {
		if (isMerge(entry) || (entry.DeclaredByRule && isInsert(entry))) && eqi(deref(entry.Name), anchor) && entry.Name != nil {
			if !entry.Values.isObj() {
				entry.Values = newObj()
			}
			entry.Values.set("layoutConfig", placement)
			return
		}
	}
	for _, entry := range *elementMap {
		if isDrop(entry) && entry.Name != nil && eqi(*entry.Name, anchor) {
			return
		}
	}
	values := newObj()
	values.set("layoutConfig", placement)
	*elementMap = append(*elementMap, &mapEntry{Operation: OperationMerge, Name: strp(anchor), Values: values})
}
