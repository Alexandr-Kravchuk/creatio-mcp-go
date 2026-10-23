#pragma warning disable CS8618, // Non-nullable field is uninitialized.

using ATF.Repository;
using ATF.Repository.Attributes;
using System;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;
using System.ComponentModel.DataAnnotations;

namespace Acme.Models {
	[ExcludeFromCodeCoverage]
	[Schema("ContactType")]
	public class ContactType: BaseModel {

		#region Schema Columns

		/// <summary>
		/// Name
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[MinLength(1, ErrorMessage = "The {0} field must be a minimum of {1} character long.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Name")]
		public string Name { get; set; }

		/// <summary>
		/// Description
		/// </summary>
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Description")]
		public string Description { get; set; }

		#endregion

		#region Details

		/// <summary>
		/// Collection of Contact by column TypeId
		/// <remarks>
		/// <see cref="global::Acme.Models.Contact">See more about the Contact model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Contact.TypeId))]
		public virtual List<Contact> CollectionOfContactByType { get; set; }

		#endregion
	}
}
#pragma warning restore CS8618 // Non-nullable field is uninitialized.
