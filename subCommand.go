package flaggy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"text/template"
	"time"
)

// Subcommand represents a subcommand which contains a set of child
// subcommands along with a set of flags relevant to it.  Parsing
// runs until a subcommand is detected by matching its name and
// position.  Once a matching subcommand is found, the next set
// of parsing occurs within that matched subcommand.
type Subcommand struct {
	Name                  string
	ShortName             string
	Description           string
	Position              int // the position of this subcommand, not including flags
	Subcommands           []*Subcommand
	Flags                 []*Flag
	PositionalFlags       []*PositionalValue
	AdditionalHelpPrepend string             // additional prepended message when Help is displayed
	AdditionalHelpAppend  string             // additional appended message when Help is displayed
	Used                  bool               // indicates this subcommand was found and parsed
	HelpTemplate          *template.Template // template for Help output
	Hidden                bool               // indicates this subcommand should be hidden from help
}

// NewSubcommand creates a new subcommand that can have flags or PositionalFlags
// added to it.  The position starts with 1, not 0
func NewSubcommand(name string) *Subcommand {
	newSC := &Subcommand{
		Name: name,
	}
	newSC.SetHelpTemplate(DefaultHelpTemplate)
	return newSC
}

// SetHelpTemplate sets the go template this parser will use when rendering
// Help.
func (sc *Subcommand) SetHelpTemplate(tmpl string) error {
	var err error
	sc.HelpTemplate = template.New("Help")
	sc.HelpTemplate, err = sc.HelpTemplate.Parse(tmpl)
	if err != nil {
		return err
	}
	return nil
}

// parseAllFlagsFromArgs parses the non-positional flags such as -f or -v=value
// out of the supplied args and returns the positional items in order.
func (sc *Subcommand) parseAllFlagsFromArgs(p *Parser, args []string) ([]string, bool, error) {

	var err error
	var positionalOnlyArguments []string
	var helpRequested bool // indicates the user has supplied -h and we
	// should render help if we are the last subcommand

	// indicates we should skip the next argument, like when parsing a flag
	// that separates key and value by space
	var skipNext bool

	// endArgfound indicates that a -- was found and everything
	// remaining should be added to the trailing arguments slices
	var endArgFound bool

	// find all the normal flags (not positional) and parse them out
	for i, a := range args {

		// evaluate if there is a following arg to avoid panics
		var nextArgExists bool
		var nextArg string
		if len(args)-1 >= i+1 {
			nextArgExists = true
			nextArg = args[i+1]
		}

		// if end arg -- has been found, just add everything to TrailingArguments
		if endArgFound {
			p.TrailingArguments = append(p.TrailingArguments, a)
			continue
		}

		// skip this run if specified
		if skipNext {
			skipNext = false
			// debugPrint("Skipping arg", a)
			continue
		}

		// parse the flag into its name for consideration without dashes
		flagName := parseFlagToName(a)

		// if the flag being passed is version or v and the option to display
		// version with version flags, then display version
		if p.ShowVersionWithVFlag {
			if flagName == "v" || flagName == "version" {
				p.ShowVersionAndExit()
			}
		}

		// if the show Help on h flag option is set, then show Help when h or Help
		// is passed as an option
		if p.ShowHelpWithHFlag {
			if flagName == "h" || flagName == "help" {
				// Ensure this is the last subcommand passed so we give the correct
				// help output
				helpRequested = true
				continue
			}
		}

		// determine what kind of flag this is
		argType := determineArgType(a)

		// strip flags from arg
		// debugPrint("Parsing flag named", a, "of type", argType)

		// depending on the flag type, parse the key and value out, then apply it
		switch argType {
		case argIsFinal:
			// debugPrint("Arg", i, "is final:", a)
			endArgFound = true
		case argIsPositional:
			// debugPrint("Arg is positional or subcommand:", a)
			// Add this positional argument into a slice of their own, so that
			// we can determine if its a subcommand or positional value later
			positionalOnlyArguments = append(positionalOnlyArguments, a)
		case argIsFlagWithSpace:
			a = parseFlagToName(a)
			// debugPrint("Arg", i, "is flag with space:", a)
			skipNext = true
			// parse next arg as value to this flag and apply to subcommand flags
			// if the flag is a bool flag, then we check for a following positional
			// and skip it if necessary
			if flagIsBool(sc, p, a) {
				switch {
				case nextArgExists && nextArg == "true":
					_, err = setValueForParsers(a, "true", p, sc)
				case nextArgExists && nextArg == "false":
					_, err = setValueForParsers(a, "false", p, sc)
				default:
					// if the next value was not true or false, we assume this bool
					// flag stands alone and should be assumed to mean true.  In this
					// case, we do not skip the next flag in the argument list.
					skipNext = false
					_, err = setValueForParsers(a, "true", p, sc)
				}

				// if an error occurs, just return it and quit parsing
				if err != nil {
					return []string{}, false, err
				}
				// by default, we just assign the next argument to the value and continue
				continue
			}

			// if the next arg was not found, then show a Help message
			if !nextArgExists {
				sc.ShowHelpWithMessage("Expected a following arg for flag " + a + ", but it did not exist.")
				os.Exit(2)
			}
			_, err = setValueForParsers(a, nextArg, p, sc)
			if err != nil {
				return []string{}, false, err
			}
		case argIsFlagWithValue:
			// debugPrint("Arg", i, "is flag with value:", a)
			a = parseFlagToName(a)
			// parse flag into key and value and apply to subcommand flags
			key, val := parseArgWithValue(a)
			_, err = setValueForParsers(key, val, p, sc)
			if err != nil {
				return []string{}, false, err
			}
			// if this flag type was found and not set, and the parser is set to show
			// Help when an unknown flag is found, then show Help and exit.
		}

	}

	return positionalOnlyArguments, helpRequested, nil
}

// Parse causes the argument parser to parse based on the supplied []string.
// depth specifies the non-flag subcommand positional depth
func (sc *Subcommand) parse(p *Parser, args []string, depth int) error {

	debugPrint("- Parsing subcommand", sc.Name, "with depth of", depth, "and args", args)

	// if a command is parsed, its used
	sc.Used = true

	// Parse the normal flags out of the argument list and retain the positionals.
	// Apply the flags to the parent parser and the current subcommand context.
	positionalOnlyArguments, helpRequested, err := sc.parseAllFlagsFromArgs(p, args)
	if err != nil {
		return err
	}

	// loop over positional values and look for their matching positional
	// parameter, or their positional command.  If neither are found, then
	// we throw an error
	var parsedArgCount int
	for pos, v := range positionalOnlyArguments {

		// the first relative positional argument will be human natural at position 1
		// but offset for the depth of relative commands being parsed for currently.
		relativeDepth := pos - depth + 1
		// debugPrint("Parsing positional only position", relativeDepth, "with value", v)

		if relativeDepth < 1 {
			debugPrint("skipped value", v)
			continue
		}
		parsedArgCount++
		// determine subcommands and parse them by positional value and name
		for _, cmd := range sc.Subcommands {
			// debugPrint("Subcommand being compared", relativeDepth, "==", cmd.Position, "and", v, "==", cmd.Name, "==", cmd.ShortName)
			if relativeDepth == cmd.Position && (v == cmd.Name || v == cmd.ShortName) {
				debugPrint("Decending into positional subcommand", cmd.Name, "at relativeDepth", relativeDepth, "and absolute depth", depth+1)
				return cmd.parse(p, args, depth+parsedArgCount) // continue recursive positional parsing
			}
		}

		// determine positional args and parse them by positional value and name
		var foundPositional bool
		for _, val := range sc.PositionalFlags {
			if relativeDepth == val.Position {
				debugPrint("Found a positional value at relativePos:", relativeDepth, "value:", v)
				// defrerence the struct pointer, then set the pointer property within it
				*val.AssignmentVar = v
				// debugPrint("set positional to value", *val.AssignmentVar)
				foundPositional = true
				val.Found = true
				break
			}
		}

		// if there aren't any positional flags but there are sub subcommands that
		// were not used, display a useful message with subcommand options.
		if !foundPositional {
			debugPrint("No positional at position", relativeDepth)
			var foundSubcommandAtDepth bool
			for _, cmd := range sc.Subcommands {
				if cmd.Position == relativeDepth {
					foundSubcommandAtDepth = true
				}
			}

			// if there is a subcommand here but it was not specified, display them all
			// as a suggestion to the user before exiting.
			if foundSubcommandAtDepth {
				fmt.Fprintln(os.Stderr, "Subcommand or positional value not found at depth", strconv.Itoa(relativeDepth)+".  Available subcommands:")
				var output string
				for _, cmd := range sc.Subcommands {
					if cmd.Hidden {
						continue
					}
					output = output + " " + cmd.Name
				}
				fmt.Fprintln(os.Stderr, output) // follow up with a newline
				os.Exit(2)
			}

			// if there were not any flags or subcommands at this position at all, then
			// throw an error (display Help if necessary)
			sc.ShowHelpWithMessage("Unexpected argument: " + v)
			os.Exit(2)
		}
	}

	// if help was requested and we should show help when h is passed,
	if helpRequested && p.ShowHelpWithHFlag {
		sc.ShowHelp()
		os.Exit(0)
	}

	// find any positionals that were not used on subcommands that were
	// found and throw help (unknown argument)
	for _, pv := range p.PositionalFlags {
		if pv.Required && !pv.Found {
			p.ShowHelpWithMessage("Required global positional variable " + pv.Name + " not found at position " + strconv.Itoa(pv.Position))
			os.Exit(2)
		}
	}
	for _, pv := range sc.PositionalFlags {
		if pv.Required && !pv.Found {
			sc.ShowHelpWithMessage("Required positional of subcommand " + sc.Name + " named " + pv.Name + " not found at position " + strconv.Itoa(pv.Position))
			os.Exit(2)
		}
	}

	return nil
}

// FlagExists lets you know if the flag name exists as either a short or long
// name in the (sub)command
func (sc *Subcommand) FlagExists(name string) bool {

	for _, f := range sc.Flags {
		if f.HasName(name) {
			return true
		}
	}

	return false
}

// AddSubcommand adds a possible subcommand to the Parser.
func (sc *Subcommand) AddSubcommand(newSC *Subcommand, relativePosition int) error {

	// assign the depth of the subcommand when its attached
	newSC.Position = relativePosition

	// ensure no subcommands at this depth with this name
	for _, other := range sc.Subcommands {
		if newSC.Position == other.Position {
			if newSC.Name != "" {
				if newSC.Name == other.Name {
					return errors.New("Unable to add subcommand because one already exists at position" + strconv.Itoa(newSC.Position) + " with name " + other.Name)
				}
			}
			if newSC.ShortName != "" {
				if newSC.ShortName == other.ShortName {
					return errors.New("Unable to add subcommand because one already exists at position" + strconv.Itoa(newSC.Position) + " with name " + other.ShortName)
				}
			}
		}
	}

	// ensure no positionals at this depth
	for _, other := range sc.PositionalFlags {
		if newSC.Position == other.Position {
			return errors.New("Unable to add subcommand because a positional value already exists at position " + strconv.Itoa(newSC.Position) + ": " + other.Name)
		}
	}

	sc.Subcommands = append(sc.Subcommands, newSC)

	return nil
}

// addFlag is a generic to add flags of any type
func (sc *Subcommand) addFlag(assignmentVar interface{}, shortName string, longName string, description string) error {

	// if the flag is already used, throw an error
	for _, existingFlag := range sc.Flags {
		if longName != "" && existingFlag.LongName == longName {
			return errors.New("Flag " + longName + " added to subcommand " + sc.Name + " but the name isalready assigned.")
		}
		if shortName != "" && existingFlag.ShortName == shortName {
			return errors.New("Flag " + shortName + " added to subcommand " + sc.Name + " but the short name is already assigned.")
		}
	}

	newFlag := Flag{}
	newFlag.AssignmentVar = assignmentVar
	newFlag.ShortName = shortName
	newFlag.LongName = longName
	newFlag.Description = description
	sc.Flags = append(sc.Flags, &newFlag)

	return nil
}

// AddStringFlag adds a new string flag
func (sc *Subcommand) AddStringFlag(assignmentVar *string, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddStringSliceFlag adds a new slice of strings flag
// Specify the flag multiple times to fill the slice
func (sc *Subcommand) AddStringSliceFlag(assignmentVar *[]string, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddBoolFlag adds a new bool flag
func (sc *Subcommand) AddBoolFlag(assignmentVar *bool, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddBoolSliceFlag adds a new slice of bools flag
// Specify the flag multiple times to fill the slice
func (sc *Subcommand) AddBoolSliceFlag(assignmentVar *[]bool, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddByteSliceFlag adds a new slice of bytes flag
// Specify the flag multiple times to fill the slice.  Takes hex as input.
func (sc *Subcommand) AddByteSliceFlag(assignmentVar *[]byte, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddDurationFlag adds a new time.Duration flag.
// Input format is described in time.ParseDuration().
// Example values: 1h, 1h50m, 32s
func (sc *Subcommand) AddDurationFlag(assignmentVar *time.Duration, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddDurationSliceFlag adds a new time.Duration flag.
// Input format is described in time.ParseDuration().
// Example values: 1h, 1h50m, 32s
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddDurationSliceFlag(assignmentVar *[]time.Duration, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddFloat32Flag adds a new float32 flag.
func (sc *Subcommand) AddFloat32Flag(assignmentVar *float32, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddFloat32SliceFlag adds a new float32 flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddFloat32SliceFlag(assignmentVar *[]float32, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddFloat64Flag adds a new float64 flag.
func (sc *Subcommand) AddFloat64Flag(assignmentVar *float64, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddFloat64SliceFlag adds a new float64 flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddFloat64SliceFlag(assignmentVar *[]float64, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddIntFlag adds a new int flag
func (sc *Subcommand) AddIntFlag(assignmentVar *int, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddIntSliceFlag adds a new int slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddIntSliceFlag(assignmentVar *[]int, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUIntFlag adds a new uint flag
func (sc *Subcommand) AddUIntFlag(assignmentVar *uint, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUIntSliceFlag adds a new uint slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddUIntSliceFlag(assignmentVar *[]uint, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt64Flag adds a new uint64 flag
func (sc *Subcommand) AddUInt64Flag(assignmentVar *uint64, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt64SliceFlag adds a new uint64 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddUInt64SliceFlag(assignmentVar *[]uint64, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt32Flag adds a new uint32 flag
func (sc *Subcommand) AddUInt32Flag(assignmentVar *uint32, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt32SliceFlag adds a new uint32 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddUInt32SliceFlag(assignmentVar *[]uint32, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt16Flag adds a new uint16 flag
func (sc *Subcommand) AddUInt16Flag(assignmentVar *uint16, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt16SliceFlag adds a new uint16 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddUInt16SliceFlag(assignmentVar *[]uint16, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt8Flag adds a new uint8 flag
func (sc *Subcommand) AddUInt8Flag(assignmentVar *uint8, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddUInt8SliceFlag adds a new uint8 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddUInt8SliceFlag(assignmentVar *[]uint8, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt64SliceFlag adds a new int64 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddInt64SliceFlag(assignmentVar *[]int64, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt32Flag adds a new int32 flag
func (sc *Subcommand) AddInt32Flag(assignmentVar *int32, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt32SliceFlag adds a new int32 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddInt32SliceFlag(assignmentVar *[]int32, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt16Flag adds a new int16 flag
func (sc *Subcommand) AddInt16Flag(assignmentVar *int16, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt16SliceFlag adds a new int16 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddInt16SliceFlag(assignmentVar *[]int16, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt8Flag adds a new int8 flag
func (sc *Subcommand) AddInt8Flag(assignmentVar *int8, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddInt8SliceFlag adds a new int8 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddInt8SliceFlag(assignmentVar *[]int8, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddIPFlag adds a new net.IP flag.
func (sc *Subcommand) AddIPFlag(assignmentVar *net.IP, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddIPSliceFlag adds a new int8 slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddIPSliceFlag(assignmentVar *[]net.IP, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddHardwareAddrFlag adds a new net.HardwareAddr flag.
func (sc *Subcommand) AddHardwareAddrFlag(assignmentVar *net.HardwareAddr, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddHardwareAddrSliceFlag adds a new net.HardwareAddr slice flag.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddHardwareAddrSliceFlag(assignmentVar *[]net.HardwareAddr, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddIPMaskFlag adds a new net.IPMask flag. IPv4 Only.
func (sc *Subcommand) AddIPMaskFlag(assignmentVar *net.IPMask, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddIPMaskSliceFlag adds a new net.HardwareAddr slice flag. IPv4 only.
// Specify the flag multiple times to fill the slice.
func (sc *Subcommand) AddIPMaskSliceFlag(assignmentVar *[]net.IPMask, shortName string, longName string, description string) error {
	return sc.addFlag(assignmentVar, shortName, longName, description)
}

// AddPositionalValue adds a positional value to the subcommand.  the
// relativePosition starts at 1 and is relative to the subcommand it belongs to
func (sc *Subcommand) AddPositionalValue(assignmentVar *string, name string, relativePosition int, required bool, description string) error {

	// ensure no other positionals are at this depth
	for _, other := range sc.PositionalFlags {
		if relativePosition == other.Position {
			return errors.New("Unable to add positional value because one already exists at position: " + strconv.Itoa(relativePosition))
		}
	}

	// ensure no subcommands at this depth
	for _, other := range sc.Subcommands {
		if relativePosition == other.Position {
			return errors.New("Unable to add positional value a subcommand already exists at position: " + strconv.Itoa(relativePosition))
		}
	}

	newPositionalValue := PositionalValue{
		Name:          name,
		Position:      relativePosition,
		AssignmentVar: assignmentVar,
		Required:      required,
		Description:   description,
	}
	sc.PositionalFlags = append(sc.PositionalFlags, &newPositionalValue)

	return nil
}

// SetValueForKey sets the value for the specified key. If setting a bool
// value, then send "true" or "false" as strings.  The returned bool indicates
// that a value was set.
func (sc *Subcommand) SetValueForKey(key string, value string) (bool, error) {

	// debugPrint("Looking to set key", key, "to value", value)
	// check for and assign flags that match the key
	for _, f := range sc.Flags {
		// debugPrint("Evaluating string flag", f.ShortName, "==", key, "||", f.LongName, "==", key)
		if f.ShortName == key || f.LongName == key {
			// debugPrint("Setting string value for", key, "to", value)
			f.identifyAndAssignValue(value)
			return true, nil
		}
	}

	// debugPrint(sc.Name, "was unable to find a key named", key, "to set to value", value)
	return false, nil
}

// ShowHelp shows Help without an error message
func (sc *Subcommand) ShowHelp() {
	debugPrint("showing help for", sc.Name)
	sc.ShowHelpWithMessage("")
}

// ShowHelpWithMessage shows the Help for this parser with an optional string error
// message as a header.  The supplied subcommand will be the context of Help
// displayed to the user.
func (sc *Subcommand) ShowHelpWithMessage(message string) {

	// create a new Help values template and extract values into it
	help := Help{}
	help.ExtractValues(sc, message)
	err := sc.HelpTemplate.Execute(os.Stderr, help)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error rendering Help template:", err)
	}
}
